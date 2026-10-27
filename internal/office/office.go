// Package office is «Документы онлайн»: documents of the drive opened in ONLYOFFICE Document Server (an own server
// in Docker). go24 decides who may open or edit a file and signs the editor's config; the browser loads the editor
// from the document server through go24 (/office/…); the document server fetches the file from go24 and, when
// everyone has closed it, calls go24 back with the edited file, which go24 keeps as the file's new version.
package office

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

// Server is a document server.
type Server struct {
	URL    string // the document server as go24 reaches it (http://onlyoffice)
	Secret string // its JWT secret (JWT_SECRET of the document server)
	Self   string // go24 as the document server reaches it (http://go24:8080)
}

// New is nil when documents online are not configured.
func New(url, secret, self string) *Server {
	if url == "" || secret == "" || self == "" {
		return nil
	}
	return &Server{URL: strings.TrimRight(url, "/"), Secret: secret, Self: strings.TrimRight(self, "/")}
}

// Kind is the editor of a file: word, cell, slide or pdf; "" — the document server does not open it.
func Kind(name string) string {
	switch Ext(name) {
	case "docx", "doc", "docm", "dotx", "odt", "ott", "rtf", "txt", "fodt":
		return "word"
	case "xlsx", "xls", "xlsm", "xltx", "ods", "ots", "csv", "fods":
		return "cell"
	case "pptx", "ppt", "pptm", "potx", "odp", "otp", "fodp":
		return "slide"
	case "pdf":
		return "pdf"
	}
	return ""
}

// Editable: the document server edits the file in its own format (the others it only shows).
func Editable(name string) bool {
	switch Ext(name) {
	case "docx", "xlsx", "pptx":
		return true
	}
	return false
}

// Ext is the file's extension, lower case, without the dot.
func Ext(name string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
}

// Key names one state of a file for the document server: the same key — the same document, edited together.
func Key(fileID, objectKey string) string {
	sum := sha256.Sum256([]byte(objectKey))
	return fileID + "-" + hex.EncodeToString(sum[:6])
}

// Sign makes a JWT (HS256) of the claims with the secret.
func (s *Server) Sign(claims any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(body)
	return unsigned + "." + enc.EncodeToString(s.mac(unsigned)), nil
}

func (s *Server) mac(text string) []byte {
	m := hmac.New(sha256.New, []byte(s.Secret))
	m.Write([]byte(text))
	return m.Sum(nil)
}

// ErrToken: the token is not the document server's.
var ErrToken = errors.New("office: bad token")

// Verify reads the claims of a JWT signed with the secret.
func (s *Server) Verify(token string, claims any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, s.mac(parts[0]+"."+parts[1])) {
		return ErrToken
	}
	var header struct {
		Alg string `json:"alg"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil || header.Alg != "HS256" {
		return ErrToken
	}
	if raw, err = base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return ErrToken
	}
	var exp struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &exp) == nil && exp.Exp != 0 && time.Now().Unix() > exp.Exp {
		return ErrToken
	}
	return json.Unmarshal(raw, claims)
}

// Stamp signs what go24 hands the document server in a link (the file to fetch, where to call back).
func (s *Server) Stamp(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString(s.mac("link\n" + strings.Join(parts, "\n")))[:32]
}

// Stamped: the stamp is go24's for the parts.
func (s *Server) Stamped(stamp string, parts ...string) bool {
	return hmac.Equal([]byte(stamp), []byte(s.Stamp(parts...)))
}

// Callback is what the document server says about a document (with JWT on, the same is in Token).
type Callback struct {
	Key    string   `json:"key"`
	Status int      `json:"status"`
	URL    string   `json:"url"`
	Users  []string `json:"users"`
	Token  string   `json:"token"`
}

// Statuses of a callback: the document is being edited (by Users), ready to be saved (everyone closed it), could not
// be saved, closed with no changes, saved by «Сохранить» (forcesave), or could not be saved so.
const (
	StatusEditing        = 1
	StatusSave           = 2
	StatusSaveError      = 3
	StatusClosed         = 4
	StatusForceSave      = 6
	StatusForceSaveError = 7
)

// Inside is the address go24 fetches an edited file from: the document server's own links go through go24's
// /office/ (the browser's address), go24 takes them from the document server directly.
func (s *Server) Inside(link string) string {
	if i := strings.Index(link, "/office/"); i >= 0 && strings.HasPrefix(link, "http") {
		return s.URL + link[i+len("/office"):]
	}
	return link
}

// ErrConvert: the document server could not convert the file (its error code is in the text).
var ErrConvert = errors.New("office: conversion failed")

// Convert has the document server make a file of another format (pdf) of the file at fileURL (a link the document
// server fetches: go24's, signed); key names this state of the file, so a repeated request is answered from the
// document server's cache. The result is the converted file's link, for go24 to fetch (Inside).
func (s *Server) Convert(ctx context.Context, fileURL, fileType, key, title, outputType string) (string, error) {
	return s.convert(ctx, map[string]any{"async": false, "filetype": fileType, "key": key, "outputtype": outputType, "title": title, "url": fileURL})
}

// Thumbnail has the document server draw the first page of the file at fileURL as a PNG whose longest side is side
// (the thumbnail of a document on «Диск»); the rest as of Convert.
func (s *Server) Thumbnail(ctx context.Context, fileURL, fileType, key, title string, side int) (string, error) {
	return s.convert(ctx, map[string]any{"async": false, "filetype": fileType, "key": key + "-t", "outputtype": "png", "title": title,
		"url": fileURL, "thumbnail": map[string]any{"aspect": 1, "first": true, "width": side, "height": side}})
}

// convert asks the document server's ConvertService for the conversion req and gives the converted file's link.
func (s *Server) convert(ctx context.Context, req map[string]any) (string, error) {
	token, err := s.Sign(req)
	if err != nil {
		return "", err
	}
	req["token"] = token
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/converter", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(hr)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		EndConvert bool   `json:"endConvert"`
		FileURL    string `json:"fileUrl"`
		Error      int    `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("%w: %s", ErrConvert, res.Status)
	}
	if out.Error != 0 || !out.EndConvert || out.FileURL == "" {
		return "", fmt.Errorf("%w: error %d", ErrConvert, out.Error)
	}
	return s.Inside(out.FileURL), nil
}
