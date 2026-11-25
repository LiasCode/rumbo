// Command zipdir writes a zip file of a directory, the directory itself at
// its top, for the Windows archives of scripts/release.sh. It is written in
// Go so that a release needs nothing that Go does not bring.
//
//	go run ./scripts/zipdir <archive.zip> <directory>
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: zipdir <archive.zip> <directory>")
		os.Exit(2)
	}

	if err := write(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "zipdir:", err)
		os.Exit(1)
	}
}

func write(archive, dir string) error {
	out, err := os.Create(archive)

	if err != nil {
		return err
	}

	defer out.Close()

	zipped := zip.NewWriter(out)
	parent := filepath.Dir(filepath.Clean(dir))

	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		return add(zipped, parent, path)
	})

	if err != nil {
		return err
	}

	if err := zipped.Close(); err != nil {
		return err
	}

	return out.Close()
}

// add puts the file at path in the archive, named from parent down.
func add(zipped *zip.Writer, parent, path string) error {
	info, err := os.Stat(path)

	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)

	if err != nil {
		return err
	}

	name, err := filepath.Rel(parent, path)

	if err != nil {
		return err
	}

	header.Name = filepath.ToSlash(name)
	header.Method = zip.Deflate

	writer, err := zipped.CreateHeader(header)

	if err != nil {
		return err
	}

	file, err := os.Open(path)

	if err != nil {
		return err
	}

	defer file.Close()

	_, err = io.Copy(writer, file)

	return err
}
