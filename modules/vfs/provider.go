package vfs

import (
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/afero"
)

func (o *Overlay) OpenFilesystem() (afero.Fs, error) {
	fs, err := o.openRawFilesystem()
	if err != nil {
		return nil, err
	}
	wd := o.WorkDir
	if wd == "" {
		wd = "/"
	}
	return afero.NewBasePathFs(fs, wd), nil
}

// opens the filesystem before changing the working dir
func (o *Overlay) openRawFilesystem() (afero.Fs, error) {
	u, err := url.Parse(o.Root)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file", "":
		return o.openFile(u)
	case "http", "https":
		return o.openHttp(u)
	default:
		return nil, fmt.Errorf("unrecognized scheme: %s", u.Scheme)
	}
}

func (o *Overlay) openFile(u *url.URL) (afero.Fs, error) {
	// see if its a directory
	info, err := os.Stat(u.Path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return afero.NewBasePathFs(afero.NewOsFs(), u.Path), nil
	}
	ft := o.Type
	if ft == "" {
		ft = filetypeFromName(u.String())
	}
	file, err := os.Open(u.Path)
	if err != nil {
		return nil, err
	}
	fs, err := filesystemFromReader(ft, file)
	if err != nil {
		return nil, err
	}
	return fs, nil
}

func (o *Overlay) openHttp(u *url.URL) (afero.Fs, error) {
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range o.Headers {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unable to get network resource: %s", resp.Status)
	}
	ft := o.Type
	// TODO: perhaps we can also introspect via http content type?
	if ft == "" {
		ft = filetypeFromName(u.String())
	}
	fs, err := filesystemFromReader(ft, resp.Body)
	if err != nil {
		return nil, err
	}
	return fs, nil

}
