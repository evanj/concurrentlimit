package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const version = "3.11.4"
const protocURLTemplate = "https://github.com/protocolbuffers/protobuf/releases/download/v%s/protoc-%s-%s-x86_64.zip"
const protocZipPath = "bin/protoc"
const includeZipPath = "include/"

var goosToProtocOS = map[string]string{
	"darwin": "osx",
	"linux":  "linux",
}

func shouldExtract(name string) bool {
	return !strings.HasSuffix(name, "/") &&
		(name == protocZipPath || strings.HasPrefix(name, includeZipPath))
}

func extractFromZip(outputDir string, f *zip.File) error {
	outputPath := filepath.Join(outputDir, f.Name)
	log.Printf("writing %s ...", outputPath)
	basePath := filepath.Dir(outputPath)
	err := os.MkdirAll(basePath, 0700)
	if err != nil {
		return err
	}
	outputFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
	if err != nil {
		return err
	}
	defer outputFile.Close()

	fileReader, err := f.Open()
	if err != nil {
		return err
	}
	defer fileReader.Close()

	_, err = io.Copy(outputFile, fileReader)
	return err
}

func main() {
	outputDir := flag.String("outputDir", "", "Path were to write bin/protoc and include/*")
	flag.Parse()

	protocURL := fmt.Sprintf(protocURLTemplate, version, version, goosToProtocOS[runtime.GOOS])
	log.Printf("downloading protoc from %s ...", protocURL)
	resp, err := http.Get(protocURL)
	if err != nil {
		panic(err)
	}
	protocZipBytes, err := ioutil.ReadAll(resp.Body)
	err2 := resp.Body.Close()
	if err != nil {
		panic(err)
	}
	if err2 != nil {
		panic(err2)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(protocZipBytes), int64(len(protocZipBytes)))
	if err != nil {
		panic(err)
	}
	for _, f := range zipReader.File {
		if shouldExtract(f.Name) {
			err = extractFromZip(*outputDir, f)
			if err != nil {
				panic(err)
			}
		}
	}
}
