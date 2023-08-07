// Copyright (c) 2023 Michael D Henderson. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the COPYING file.

// Package main implements a wayback archive updater.
package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"github.com/btcsuite/btcutil/base58"
	"github.com/gabriel-vasile/mimetype"
	"golang.org/x/net/html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	public, data := "../../playbymail.net", ""
	flag.StringVar(&data, "data", data, "location of data files")
	flag.StringVar(&public, "public", public, "location of public files")
	flag.Parse()

	started := time.Now()
	err := run(public, data)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("completed in %v\n", time.Now().Sub(started))
}

func run(site, data string) error {
	var err error
	s := &server{
		files: make(map[string]*fileinfo_t),
		pfx:   []string{"http://playbymail.net/", "https://playbymail.net/"},
	}
	s.public, err = filepath.Abs(site)
	if err != nil {
		return err
	}
	s.data, err = filepath.Abs(data)
	if err != nil {
		return err
	}

	err = s.cacheFiles()
	if err != nil {
		return err
	}

	if data != "" {
		bb, kinds, sums := &bytes.Buffer{}, make(map[string]bool), make(map[string][]string)
		for _, fi := range s.files {
			_, _ = fmt.Fprintf(bb, "%-30s %s\n", fi.cksum, fi.name)
			if x, ok := sums[fi.cksum]; ok {
				sums[fi.cksum] = append(x, fi.name)
			} else {
				sums[fi.cksum] = []string{fi.name}
			}
			kinds[fi.kind] = true
		}
		err = os.WriteFile(filepath.Join(s.data, "b58.sums"), bb.Bytes(), 0644)
		if err != nil {
			return err
		}
		log.Printf("created %s\n", filepath.Join(s.data, "b58.sums"))

		bb = &bytes.Buffer{}
		for k, v := range sums {
			if len(v) > 1 {
				_, _ = fmt.Fprintf(bb, "%s\n", k)
				for _, vv := range v {
					_, _ = fmt.Fprintf(bb, "  %s\n", vv)
				}
			}
		}
		err = os.WriteFile(filepath.Join(s.data, "b58.dups"), bb.Bytes(), 0644)
		if err != nil {
			return err
		}
		log.Printf("created %s\n", filepath.Join(s.data, "b58.dups"))

		bb = &bytes.Buffer{}
		for k := range kinds {
			_, _ = fmt.Fprintf(bb, "%s\n", k)
		}
		err = os.WriteFile(filepath.Join(s.data, "b58.kinds"), bb.Bytes(), 0644)
		if err != nil {
			return err
		}
		log.Printf("created %s\n", filepath.Join(s.data, "b58.kinds"))
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/", s.handleGet)

	err = os.Chdir(s.public)
	if err != nil {
		return err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	log.Printf("working directory now %q\n", pwd)

	return http.ListenAndServe(":8080", s.mux)
}

type fileinfo_t struct {
	name  string
	cksum string
	kind  string
	mod   time.Time
}

type server struct {
	files  map[string]*fileinfo_t
	mux    *http.ServeMux
	public string
	data   string
	pfx    []string
}

func (s *server) cacheFiles() error {
	err := os.Chdir(s.public)
	if err != nil {
		return err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	log.Printf("working directory now %q\n", pwd)

	s.files = make(map[string]*fileinfo_t)

	err = filepath.WalkDir(".", func(path string, file fs.DirEntry, err error) error {
		if err != nil {
			return err
		} else if file.IsDir() {
			// ignore
		} else {
			info, err := file.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h := sha1.New()
			h.Write(data)
			encoded := base58.Encode(h.Sum(nil))
			fi := &fileinfo_t{name: path, cksum: string(encoded), mod: info.ModTime(), kind: filetype(path, data)}
			s.files[fi.name] = fi
		}
		return nil
	})

	return err
}

func (s *server) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	//log.Printf("%s %s\n", r.Method, r.URL)
	rpath := strings.TrimSuffix(strings.TrimPrefix(r.URL.String(), "/"), "/")
	file, ok := s.files[rpath]
	if !ok {
		// try with commas and pipes
		cpath := strings.Replace(rpath, "%2C", ",", -1)
		cpath = strings.Replace(cpath, "%7C", "|", -1)
		file, ok = s.files[cpath]
	}
	if !ok {
		// try with index.html
		if file, ok = s.files[rpath+"/index.html"]; ok {
			//log.Printf("%s %s: redirect %s %s\n", r.Method, r.URL, rpath, "index.html")
			http.Redirect(w, r, "/"+rpath+"/index.html", http.StatusFound)
			return
		}
	}
	if !ok {
		log.Printf("%s %s not found\n", r.Method, rpath)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	//log.Printf("%s %s: %s\n", r.Method, r.URL, file.kind)

	if !strings.HasPrefix(file.kind, "text/html") {
		fp, err := os.Open(file.name)
		if err != nil {
			log.Printf("%s not found\n", file.name)
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		defer fp.Close()
		w.Header().Add("Content-Type", file.kind)
		http.ServeContent(w, r, file.name, file.mod, fp)
		return
	}

	log.Printf("cache %d files %30s %s %s", len(s.files), file.cksum, file.name, file.kind)

	data, err := s.clean(file.name)
	if err != nil {
		log.Printf("%s: %v\n", file.name, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Add("Content-Type", file.kind)
	http.ServeContent(w, r, file.name, file.mod, data)
}

func filetype(name string, data []byte) string {
	mtype := mimetype.Detect(data)
	if mtype.String() == "application/octet-stream" {
		return http.DetectContentType(data)
	}
	if strings.HasPrefix(mtype.String(), "text/plain") {
		name, _, _ = strings.Cut(name, "?")
		switch filepath.Ext(name) {
		case ".css":
			return "text/css"
		}
	}
	return mtype.String()
}

func (s *server) clean(name string) (io.ReadSeeker, error) {
	fp, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer fp.Close()

	doc, err := html.Parse(fp)
	if err != nil {
		return nil, err
	}
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "a" {
				for i := range n.Attr {
					if n.Attr[i].Key == "href" {
						for _, pfx := range s.pfx {
							if strings.HasPrefix(n.Attr[i].Val, pfx) {
								n.Attr[i].Val = "/" + strings.TrimPrefix(n.Attr[i].Val, pfx)
								break
							}
						}
					}
				}
			} else if n.Data == "img" {
				for i := range n.Attr {
					if n.Attr[i].Key == "src" {
						for _, pfx := range s.pfx {
							if strings.HasPrefix(n.Attr[i].Val, pfx) {
								n.Attr[i].Val = "/" + strings.TrimPrefix(n.Attr[i].Val, pfx)
								break
							}
						}
					} else if n.Attr[i].Key == "srcset" {
						sources := strings.Split(n.Attr[i].Val, ",")
						var alt []string
						for _, source := range sources {
							for _, pfx := range s.pfx {
								if strings.HasPrefix(source, pfx) {
									source = "/" + strings.TrimPrefix(source, pfx)
									break
								}
							}
							alt = append(alt, source)
						}
						n.Attr[i].Val = strings.Join(alt, ",")
					}
				}
			} else if n.Data == "link" {
				for i := range n.Attr {
					if n.Attr[i].Key == "href" {
						for _, pfx := range s.pfx {
							if strings.HasPrefix(n.Attr[i].Val, pfx) {
								n.Attr[i].Val = "/" + strings.TrimPrefix(n.Attr[i].Val, pfx)
								break
							}
						}
					}
				}
			} else if n.Data == "script" {
				for i := range n.Attr {
					if n.Attr[i].Key == "src" {
						for _, pfx := range s.pfx {
							if strings.HasPrefix(n.Attr[i].Val, pfx) {
								n.Attr[i].Val = "/" + strings.TrimPrefix(n.Attr[i].Val, pfx)
								break
							}
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	bb := &bytes.Buffer{}
	err = html.Render(bb, doc)
	if err != nil {
		return nil, err
	}

	//fmt.Printf("%s\n", string(bb.Bytes()))

	return bytes.NewReader(bb.Bytes()), nil
}
