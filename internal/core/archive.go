package core

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PartSize keeps each presigned upload small enough that a retry on a slow link is cheap.
const PartSize = 40 * 1024 * 1024

// PackToFile writes a gzip-compressed tar of files (paths relative to root) to out.
// extra adds in-memory entries by name, such as CookiesFile.
func PackToFile(root string, files []FileEntry, extra map[string][]byte, out string) (size int64, sha string, err error) {
	f, err := os.Create(out)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	gz, _ := gzip.NewWriterLevel(io.MultiWriter(f, h), 6)
	tw := tar.NewWriter(gz)
	for _, fe := range files {
		if err := addFile(tw, root, fe.Rel); err != nil {
			return 0, "", fmt.Errorf("packing %s: %w", fe.Rel, err)
		}
	}
	for name, data := range extra {
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return 0, "", err
		}
		if _, err := tw.Write(data); err != nil {
			return 0, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return 0, "", err
	}
	if err := gz.Close(); err != nil {
		return 0, "", err
	}
	st, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	return st.Size(), hex.EncodeToString(h.Sum(nil)), nil
}

func addFile(tw *tar.Writer, root, rel string) error {
	src, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // vanished since the scan
		}
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: rel, Mode: 0o644, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.CopyN(tw, src, st.Size())
	return err
}

func SHA256File(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func PartAAD(id, prefix string, i int) string { return fmt.Sprintf("%s|%s|part|%d", id, prefix, i) }
func MetaAAD(id, prefix string) string      { return fmt.Sprintf("%s|%s|meta", id, prefix) }

// ForEachEncryptedPart reads file in PartSize chunks, encrypts each and passes it to fn.
func ForEachEncryptedPart(key []byte, file, id, prefix string, fn func(i, count int, data []byte) error) (int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	count := int((st.Size() + PartSize - 1) / PartSize)
	if count == 0 {
		count = 1
	}
	buf := make([]byte, PartSize)
	for i := 0; i < count; i++ {
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return 0, err
		}
		enc, err := Encrypt(key, buf[:n], PartAAD(id, prefix, i))
		if err != nil {
			return 0, err
		}
		if err := fn(i, count, enc); err != nil {
			return 0, err
		}
	}
	return count, nil
}

var extDirRe = regexp.MustCompile(`^extensions/([A-Za-z0-9_-]+)/`)

// ExtractProfile unpacks an archive into dest, accepting only paths that belong to this profile.
// A corrupted or hostile archive cannot write anywhere else. Returns how many entries were rejected.
func ExtractProfile(file, dest, profileID string, extensionIDs []string) (int, error) {
	exts := toSet(extensionIDs...)
	allowed := func(rel string) bool {
		if strings.HasPrefix(rel, "/") || path.IsAbs(rel) || strings.Contains(rel, "\\") {
			return false
		}
		for _, seg := range strings.Split(rel, "/") {
			if seg == ".." {
				return false
			}
		}
		if rel == "profiles/"+profileID+".json" || rel == CookiesFile || strings.HasPrefix(rel, "user-data/"+profileID+"/") {
			return true
		}
		m := extDirRe.FindStringSubmatch(rel)
		return m != nil && exts[m[1]]
	}
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, Errf("CORRUPT", "Archive is not valid gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	rejected := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rejected, Errf("CORRUPT", "Archive is damaged: %v", err)
		}
		rel := strings.TrimPrefix(path.Clean(hdr.Name), "./")
		if (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir) || !allowed(rel) {
			rejected++
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if r, err := filepath.Rel(dest, target); err != nil || strings.HasPrefix(r, "..") {
			rejected++
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return rejected, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return rejected, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return rejected, err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return rejected, err
		}
		out.Close()
		mt := hdr.ModTime
		if mt.IsZero() {
			mt = time.Now()
		}
		_ = os.Chtimes(target, mt, mt)
	}
	return rejected, nil
}
