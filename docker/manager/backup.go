package main

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Same formats as the phone app (Backup.kt), so backups and moves work
// between the phone and Docker in both directions.
//
//	backup: "TJBK1" | salt(16) | iv(12) | AES-256-GCM(zip), key PBKDF2-HMAC-SHA256 310 000
//	move:   "TJMV1" | iv(12) | AES-256-GCM(zip), one-time key from the move code
//
// The magic is the GCM additional data. The zip holds bridge.db(+wal/shm),
// wacall.db(+wal/shm), dp-stash.json and secrets.json.
var (
	backupMagic = []byte("TJBK1")
	moveMagic   = []byte("TJMV1")
	backupIter  = 310_000
	dbFiles     = []string{
		"bridge.db", "bridge.db-wal", "bridge.db-shm", "dp-stash.json",
		"wacall.db", "wacall.db-wal", "wacall.db-shm",
	}
)

func backupKey(pass string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, backupIter, 32)
}

// exportBackup: bridge must be stopped so the database is consistent.
func exportBackup(rt *Runtime, pass string) ([]byte, error) {
	if len([]rune(pass)) < 8 {
		return nil, errors.New("Passphrase must have at least 8 characters")
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, err := backupKey(pass, salt)
	if err != nil {
		return nil, err
	}
	plain, err := zipBridge(rt)
	if err != nil {
		return nil, err
	}
	sealed, err := seal(plain, key, backupMagic)
	if err != nil {
		return nil, err
	}
	return append(append(append([]byte{}, backupMagic...), salt...), sealed...), nil
}

// importBackup: bridge must be stopped. Replaces database and secrets.
func importBackup(rt *Runtime, data []byte, pass string) error {
	n := len(backupMagic)
	if len(data) <= n+28 || !bytes.Equal(data[:n], backupMagic) {
		return errors.New("Not a tjena bridge backup")
	}
	key, err := backupKey(pass, data[n:n+16])
	if err != nil {
		return err
	}
	plain, err := open(data[n+16:], key, backupMagic)
	if err != nil {
		return errors.New("Wrong passphrase or damaged backup")
	}
	return unzipInto(rt, plain)
}

func exportForMove(rt *Runtime, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("bad key")
	}
	plain, err := zipBridge(rt)
	if err != nil {
		return nil, err
	}
	sealed, err := seal(plain, key, moveMagic)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, moveMagic...), sealed...), nil
}

func importFromMove(rt *Runtime, data, key []byte) error {
	if len(key) != 32 {
		return errors.New("bad key")
	}
	n := len(moveMagic)
	if len(data) <= n+12 || !bytes.Equal(data[:n], moveMagic) {
		return errors.New("Not a tjena bridge transfer")
	}
	plain, err := open(data[n:], key, moveMagic)
	if err != nil {
		return errors.New("The transfer is damaged (or the code is from another move)")
	}
	return unzipInto(rt, plain)
}

func zipBridge(rt *Runtime) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, name := range dbFiles {
		f, err := os.Open(rt.path(name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		w, err := z.Create(name)
		if err == nil {
			_, err = io.Copy(w, f)
		}
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	s, err := rt.secrets.Load()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	w, err := z.Create("secrets.json")
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(data); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// seal returns iv(12) | AES-GCM(plain) with aad.
func seal(plain, key, aad []byte) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 12)
	_, _ = rand.Read(iv)
	return g.Seal(iv, iv, plain, aad), nil
}

func open(data, key, aad []byte) ([]byte, error) {
	if len(data) < 12+16 {
		return nil, errors.New("too short")
	}
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, data[:12], data[12:], aad)
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func unzipInto(rt *Runtime, plain []byte) error {
	z, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		return errors.New("damaged backup: " + err.Error())
	}
	allowed := map[string]bool{"secrets.json": true}
	for _, n := range dbFiles {
		allowed[n] = true
	}
	entries := map[string][]byte{}
	for _, f := range z.File {
		if !allowed[f.Name] {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return err
		}
		entries[f.Name] = data
	}
	sj, ok := entries["secrets.json"]
	if !ok {
		return errors.New("Backup has no secrets")
	}
	s, err := decodeJ(sj)
	if err != nil {
		return errors.New("Backup has damaged secrets")
	}
	if get(s, "bundle") == nil {
		return errors.New("Backup has no bridge setup")
	}
	if err = os.MkdirAll(rt.dataDir, 0o700); err != nil {
		return err
	}
	for _, n := range dbFiles {
		_ = os.Remove(rt.path(n))
	}
	for n, data := range entries {
		if n == "secrets.json" {
			continue
		}
		if err = os.WriteFile(rt.path(n), data, 0o600); err != nil {
			return err
		}
	}
	if err = rt.secrets.Save(s); err != nil {
		return err
	}
	_ = os.Remove(rt.path("example-config.version"))
	return nil
}
