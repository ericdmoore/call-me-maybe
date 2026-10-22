package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"callmemaybe/internal/provision"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testPhone() provision.Phone {
	return provision.Phone{
		ID: "kitchen", MAC: "ec:74:d7:88:a2:54", AdminPassword: "fixture-password",
		Model: provision.Model{ID: "grandstream-wp826"},
	}
}

func TestImporterAuthenticatesChecksIdentityAndPreservesLocalEntries(t *testing.T) {
	for _, scenario := range []string{"ok", "wrong-mac", "wrong-model", "bad-auth", "bad-challenge", "delete-manual", "rejected-import", "redirect", "oversize", "bad-json", "http-error"} {
		t.Run(scenario, func(t *testing.T) {
			var imported, loggedOut, loginAttempted bool
			book := []byte(`<AddressBook><Contact><FirstName>Fixture</FirstName></Contact></AddressBook>`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Origin") != "http://192.168.7.54" || r.Header.Get("Referer") != "http://192.168.7.54/login" {
					t.Error("missing same-origin management headers")
				}
				if r.Method == http.MethodPost && r.URL.Path != "/cgi-bin/phonebook_upload" {
					_ = r.ParseForm()
				}
				switch r.URL.Path {
				case "/cgi-bin/config_get":
					if r.URL.Query().Get("pvalues") == "333" {
						value := "0"
						if scenario == "delete-manual" {
							value = "1"
						}
						_, _ = io.WriteString(w, `{"configs":[{"pvalue":"333","value":"`+value+`"}]}`)
						return
					}
					switch scenario {
					case "redirect":
						http.Redirect(w, r, "http://example.invalid/steal", http.StatusFound)
						return
					case "oversize":
						_, _ = io.WriteString(w, strings.Repeat(" ", maxBody+1))
						return
					case "bad-json":
						_, _ = io.WriteString(w, "invalid")
						return
					case "http-error":
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					mac, model := testPhone().MAC, "WP826"
					if scenario == "wrong-mac" {
						mac = "00:11:22:33:44:55"
					}
					if scenario == "wrong-model" {
						model = "other"
					}
					_, _ = io.WriteString(w, `{"configs":[{"pvalue":"67","value":"`+mac+`"},{"pvalue":"phone_model","value":"`+model+`"}]}`)
				case "/cgi-bin/access":
					if r.Form.Get("access") != hash("admin") {
						t.Error("wrong username proof")
					}
					if scenario == "bad-challenge" {
						_, _ = io.WriteString(w, `{"response":"error","body":""}`)
						return
					}
					_, _ = io.WriteString(w, `{"response":"success","body":"nonce"}`)
				case "/cgi-bin/dologin":
					loginAttempted = true
					if r.Form.Get("username") != "admin" || r.Form.Get("password") != hash(testPhone().AdminPassword+"nonce") {
						t.Error("wrong challenge proof")
					}
					if scenario == "bad-auth" {
						_, _ = io.WriteString(w, `{"response":"error","body":"wrong4"}`)
						return
					}
					_, _ = io.WriteString(w, `{"response":"success","body":{"sid":"fixture-session"}}`)
				case "/cgi-bin/phonebook_upload":
					imported = true
					cookie, err := r.Cookie("sid")
					if err != nil || cookie.Value != "fixture-session" {
						t.Error("missing session cookie")
					}
					if err := r.ParseMultipartForm(maxBody); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					if r.FormValue("sid") != "fixture-session" || r.FormValue("fname") != "phonebook.xml" {
						t.Error("missing import fields")
					}
					f, _, err := r.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					b, _ := io.ReadAll(f)
					if string(b) != string(book) {
						t.Error("directory changed in transit")
					}
					code := "0"
					if scenario == "rejected-import" {
						code = "1"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"response": "success", "body": code})
				case "/cgi-bin/dologout":
					loggedOut = true
					_, _ = io.WriteString(w, `{"response":"success"}`)
				default:
					t.Errorf("unexpected operation %s", r.URL.Path)
				}
			}))
			defer server.Close()
			route, _ := url.Parse(server.URL)
			client := &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "192.168.7.54" {
					t.Fatal("followed a redirect away from the handset")
				}
				r.URL.Host = route.Host
				return http.DefaultTransport.RoundTrip(r)
			})}
			err := client.Upload(context.Background(), testPhone(), netip.MustParseAddr("192.168.7.54"), book)
			if (err == nil) != (scenario == "ok") {
				t.Fatalf("result = %v", err)
			}
			if scenario == "ok" && (!imported || !loggedOut) {
				t.Fatal("did not import and close session")
			}
			if (scenario == "wrong-mac" || scenario == "wrong-model") && loginAttempted {
				t.Fatal("sent proof to a different device")
			}
			if scenario == "delete-manual" && (imported || !loggedOut) {
				t.Fatal("risked deleting local contacts or left session open")
			}
			if scenario == "rejected-import" && !loggedOut {
				t.Fatal("left failed import session open")
			}
		})
	}
}

func TestImporterRejectsUnsafeTargetsAndTransportErrors(t *testing.T) {
	client := NewClient()
	used := false
	client.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		used = true
		return nil, errors.New("private response with fixture-password")
	})
	for _, target := range []string{"127.0.0.1", "8.8.8.8", "::1"} {
		if err := client.Upload(context.Background(), testPhone(), netip.MustParseAddr(target), nil); err != ErrUnavailable {
			t.Fatal(err)
		}
	}
	if used {
		t.Fatal("contacted an unsafe target")
	}
	if err := client.Upload(context.Background(), testPhone(), netip.MustParseAddr("192.168.7.54"), nil); err != ErrUnavailable || strings.Contains(err.Error(), "fixture-password") {
		t.Fatalf("unsafe error: %v", err)
	}
	if Supported("grandstream-wp816") {
		t.Fatal("claimed an unverified model")
	}
}
