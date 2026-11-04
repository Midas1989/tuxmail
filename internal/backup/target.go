// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is an archive stored at a target.
type Entry struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Target stores archives: a local directory (also a USB stick) or WebDAV
// (Nextcloud, OpenCloud). Cloud providers of the cloud module can
// implement it as well.
type Target interface {
	Put(ctx context.Context, name string, r io.Reader) error
	Get(ctx context.Context, name string) (io.ReadCloser, error)
	List(ctx context.Context) ([]Entry, error)
	Delete(ctx context.Context, name string) error
}

// TargetConfig is the stored configuration of a target.
type TargetConfig struct {
	Kind     string `json:"kind"` // local | webdav
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	// PasswordRef is the secret-store reference of the WebDAV password.
	PasswordRef string `json:"passwordRef,omitempty"`
}

func validName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("backup: invalid archive name %q", name)
	}
	return nil
}

// LocalTarget stores archives in a directory.
type LocalTarget struct{ Dir string }

// Put implements Target (atomic: temporary file + rename).
func (t LocalTarget) Put(ctx context.Context, name string, r io.Reader) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(t.Dir, ".tmp-"+name+"-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(t.Dir, name))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Get implements Target.
func (t LocalTarget) Get(_ context.Context, name string) (io.ReadCloser, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	return os.Open(filepath.Join(t.Dir, name))
}

// List implements Target.
func (t LocalTarget) List(context.Context) ([]Entry, error) {
	ents, err := os.ReadDir(t.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), Extension) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete implements Target.
func (t LocalTarget) Delete(_ context.Context, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return os.Remove(filepath.Join(t.Dir, name))
}

// WebDAVTarget stores archives in a WebDAV collection (Nextcloud:
// https://host/remote.php/dav/files/USER/Backups/).
type WebDAVTarget struct {
	URL      string
	Username string
	Password string
	Client   *http.Client
}

func (t WebDAVTarget) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: 0}
}

func (t WebDAVTarget) url(name string) (string, error) {
	u, err := url.Parse(t.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("backup: invalid WebDAV URL %q", t.URL)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.Path = path.Join(u.Path, name)
	if name == "" {
		u.Path += "/"
	}
	return u.String(), nil
}

func (t WebDAVTarget) do(ctx context.Context, method, name string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	u, err := t.url(name)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if t.Username != "" || t.Password != "" {
		req.SetBasicAuth(t.Username, t.Password)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return t.client().Do(req)
}

func statusErr(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("backup: WebDAV %s: %s %s", op, resp.Status, strings.TrimSpace(string(b)))
}

// Put implements Target. The collection is created if it does not exist.
func (t WebDAVTarget) Put(ctx context.Context, name string, r io.Reader) error {
	if err := validName(name); err != nil {
		return err
	}
	if resp, err := t.do(ctx, "MKCOL", "", nil, nil); err == nil {
		resp.Body.Close() // 201 created, 405 exists
	}
	resp, err := t.do(ctx, http.MethodPut, name, r, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return statusErr("PUT", resp)
	}
	return nil
}

// Get implements Target.
func (t WebDAVTarget) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, http.MethodGet, name, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, statusErr("GET", resp)
	}
	return resp.Body, nil
}

// Delete implements Target.
func (t WebDAVTarget) Delete(ctx context.Context, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	resp, err := t.do(ctx, http.MethodDelete, name, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return statusErr("DELETE", resp)
	}
	return nil
}

type multistatus struct {
	Responses []struct {
		Href  string `xml:"href"`
		Props []struct {
			Length   int64  `xml:"prop>getcontentlength"`
			Modified string `xml:"prop>getlastmodified"`
			Coll     *struct {
				XMLName xml.Name
			} `xml:"prop>resourcetype>collection"`
		} `xml:"propstat"`
	} `xml:"response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:getlastmodified/><d:resourcetype/></d:prop></d:propfind>`

// List implements Target.
func (t WebDAVTarget) List(ctx context.Context) ([]Entry, error) {
	resp, err := t.do(ctx, "PROPFIND", "", strings.NewReader(propfindBody), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, statusErr("PROPFIND", resp)
	}
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("backup: WebDAV PROPFIND: %w", err)
	}
	var out []Entry
	for _, r := range ms.Responses {
		href, _ := url.PathUnescape(r.Href)
		name := path.Base(strings.TrimSuffix(href, "/"))
		if !strings.HasSuffix(name, Extension) {
			continue
		}
		e := Entry{Name: name}
		for _, p := range r.Props {
			if p.Coll != nil {
				e.Name = ""
			}
			if p.Length > 0 {
				e.Size = p.Length
			}
			if t, err := http.ParseTime(p.Modified); err == nil {
				e.Modified = t
			}
		}
		if e.Name != "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
