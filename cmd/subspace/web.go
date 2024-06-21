package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/pquerna/otp"

	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	humanize "github.com/dustin/go-humanize"
	httprouter "github.com/julienschmidt/httprouter"

	"github.com/subspacecommunity/subspace/web"
)

var (
	SessionCookieName    = "__subspace_session"
	SessionCookieNameSSO = "__subspace_sso_session"
)

type Session struct {
	Admin     bool
	UserID    string
	NotBefore time.Time
	NotAfter  time.Time
}

type Web struct {
	// Internal
	w        http.ResponseWriter
	r        *http.Request
	ps       httprouter.Params
	template string

	// Default
	Backlink string
	Version  string
	Request  *http.Request
	Section  string
	Time     time.Time
	Info     Info
	Admin    bool
	SAML     *samlsp.Middleware

	User     User
	Users    []User
	Profile  Profile
	Profiles []Profile

	TargetUser     User
	TargetProfiles []Profile

	SemanticTheme string
	TempTotpKey   *otp.Key
}

func init() {
	gob.Register(Session{})
}

func Error(w http.ResponseWriter, err error) {
	logger.Error(err)

	w.WriteHeader(http.StatusInternalServerError)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, errorPageHTML+"\n")
}

func readAllWebTemplates(subfolder string) (templates []string) {
	var target = "templates"

	if subfolder != "" {
		target = path.Join(target, subfolder)
	}

	entries, err := web.Templates.ReadDir(target)
	if err != nil {
		logger.Error(err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			templates = append(templates, readAllWebTemplates(path.Join(subfolder, entry.Name()))...)
		} else {
			templates = append(templates, path.Join(target, entry.Name()))
		}
	}

	return
}

func (w *Web) HTML() {
	logger.Debugf("web: running template: %s", w.template)
	t := template.New(w.template).Funcs(template.FuncMap{
		"hasprefix": strings.HasPrefix,
		"hassuffix": strings.HasSuffix,
		"add": func(a, b int) int {
			return a + b
		},
		"bytes": func(n int64) string {
			return fmt.Sprintf("%.2f GB", float64(n)/1024/1024/1024)
		},
		"date": func(t time.Time) string {
			return t.Format(time.UnixDate)
		},
		"time": humanize.Time,
		"ssoprovider": func() string {
			if samlSP == nil {
				return ""
			}
			redirect, err := url.Parse(samlSP.ServiceProvider.GetSSOBindingLocation(saml.HTTPRedirectBinding))
			if err != nil {
				logger.Warnf("SSO redirect invalid URL: %s", err)
				return "unknown"
			}
			domain, err := publicsuffix.EffectiveTLDPlusOne(redirect.Host)
			if err != nil {
				logger.Warnf("SSO redirect invalid URL domain: %s", err)
				return "unknown"
			}
			suffix, icann := publicsuffix.PublicSuffix(domain)
			if icann {
				suffix = "." + suffix
			}
			return cases.Title(language.English).String(strings.TrimSuffix(domain, suffix))
		},
	})

	for _, filename := range readAllWebTemplates("") {
		name := strings.TrimPrefix(filename, "templates/")
		b, err := web.Templates.ReadFile(filename)
		if err != nil {
			Error(w.w, err)
			return
		}

		var tmpl *template.Template
		if name == t.Name() {
			tmpl = t
		} else {
			tmpl = t.New(name)
		}
		if _, err := tmpl.Parse(string(b)); err != nil {
			Error(w.w, err)
			return
		}
	}

	w.w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := t.Execute(w.w, w); err != nil {
		Error(w.w, err)
		return
	}
}

func (w *Web) Redirect(format string, a ...interface{}) {
	location := fmt.Sprintf(format, a...)
	http.Redirect(w.w, w.r, location, http.StatusFound)
}

func WebHandler(h func(*Web), section string) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		web := &Web{
			w:        w,
			r:        r,
			ps:       ps,
			template: section + ".html",

			Backlink:      backlink,
			Time:          time.Now(),
			Version:       version,
			Request:       r,
			Section:       section,
			Info:          config.FindInfo(),
			SAML:          samlSP,
			SemanticTheme: semanticTheme,
			TempTotpKey:   tempTotpKey,
		}

		if section == "signin" || section == "forgot" || section == "configure" {
			h(web)
			return
		}

		if !config.FindInfo().Configured {
			web.Redirect("/configure")
			return
		}

		// Has an existing session.
		if session, _ := ValidateSession(r); session != nil {
			if session.UserID != "" {
				user, err := config.FindUser(session.UserID)
				if err != nil {
					signoutHandler(web)
					return
				}
				web.User = user
				web.Admin = user.Admin
			} else {
				web.Admin = session.Admin
			}
			h(web)
			return
		}

		// Needs a new session.
		if samlSP != nil {
			session, err := samlSP.Session.GetSession(r)

			if err != nil {
				logger.Debugf("SAML: Unable to get session from requests: %+v", err)
			}

			if session != nil {
				r = r.WithContext(samlsp.ContextWithSession(r.Context(), session))
				jwtSessionClaims, ok := session.(samlsp.JWTSessionClaims)

				if !ok {
					Error(w, fmt.Errorf("unable to decode session into JWTSessionClaims"))
					return
				}

				email := jwtSessionClaims.Subject
				if email == "" {
					Error(w, fmt.Errorf("SAML token missing email"))
					return
				}

				logger.Infof("SAML: finding user with email %q", email)
				user, err := config.FindUserByEmail(email)
				if err != nil && err != ErrUserNotFound {
					Error(w, err)
					return
				}

				if user.ID == "" {
					logger.Infof("SAML: creating user with email %q", email)
					user, err = config.AddUser(email)
					if err != nil {
						Error(w, err)
						return
					}
				}

				web.User = user
				web.Admin = user.Admin
				if err := web.SigninSession(false, user.ID); err != nil {
					Error(web.w, err)
					return
				}

				h(web)
				return
			}
		}

		logger.Warnf("auth: sign in required")
		web.Redirect("/signin")
	}
}

func Log(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		start := time.Now()
		h(w, r, ps)
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		ua := r.Header.Get("User-Agent")
		xff := r.Header.Get("X-Forwarded-For")
		xrealip := r.Header.Get("X-Real-IP")
		rang := r.Header.Get("Range")

		logger.Infof("%s %q %q %q %q %q %q %s %q %d ms", start, ip, xff, xrealip, ua, rang, r.Referer(), r.Method, r.RequestURI, int64(time.Since(start)/time.Millisecond))
	}
}

func staticHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	serveAsset(w, r, ps.ByName("path"))
}

func serveAsset(w http.ResponseWriter, r *http.Request, filename string) {
	var modTime time.Time

	b, err := web.Static.Open(path.Join("static", filename))
	if err != nil {
		logger.Debugf("static open: %s: %s", filename, err)
		http.NotFound(w, r)
		return
	}

	stat, err := b.Stat()
	if err != nil {
		logger.Error(err)
		modTime = time.Now()
	} else {
		modTime = stat.ModTime()
	}

	fi, err := io.ReadAll(b)
	if err != nil {
		Error(w, err)
		return
	}

	http.ServeContent(w, r, filename, modTime, bytes.NewReader(fi))
}

func ValidateSession(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, fmt.Errorf("auth: missing cookie")
	}
	session := &Session{}
	if err := securetoken.Decode(SessionCookieName, cookie.Value, session); err != nil {
		return nil, err
	}
	if time.Now().Before(session.NotBefore) {
		return nil, fmt.Errorf("invalid session (before valid)")
	}
	if time.Now().After(session.NotAfter) {
		return nil, fmt.Errorf("invalid session (expired session.NotAfter is %s and now is %s)", session.NotAfter, time.Now())
	}
	return session, nil
}

func (w *Web) SignoutSession() {
	if samlSP != nil {
		http.SetCookie(w.w, &http.Cookie{
			Name:     SessionCookieNameSSO,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Domain:   httpHost,
			Secure:   !httpInsecure,
			MaxAge:   -1,
			Expires:  time.Unix(1, 0),
		})
	}
	http.SetCookie(w.w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Domain:   httpHost,
		Secure:   !httpInsecure,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (w *Web) SigninSession(admin bool, userID string) error {
	expires := time.Now().Add(12 * time.Hour)

	encoded, err := securetoken.Encode(SessionCookieName, Session{
		Admin:     admin,
		UserID:    userID,
		NotBefore: time.Now(),
		NotAfter:  expires,
	})
	if err != nil {
		return fmt.Errorf("auth: encoding error: %s", err)
	}
	http.SetCookie(w.w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Domain:   httpHost,
		Secure:   !httpInsecure,
		Expires:  expires,
	})
	return nil
}
