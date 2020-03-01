package main

import (
	"flag"
	"log"
	"net/http"
)

type server struct{}

func (s *server) rawRootHandler(w http.ResponseWriter, r *http.Request) {
	err := s.rootHandler(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *server) rootHandler(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	w.Write([]byte("ok"))
	return nil
}

func main() {
	httpAddr := flag.String("httpAddr", "localhost:8080", "Address to listen for HTTP requests")
	grpcAddr := flag.String("grpcAddr", "localhost:8081", "Address to listen for gRPC requests")
	flag.Parse()

	log.Println(*httpAddr, *grpcAddr)

	s := &server{}
	http.HandleFunc("/", s.rawRootHandler)
	log.Printf("listening on http://%s ...", *httpAddr)
	err := http.ListenAndServe(*httpAddr, nil)
	if err != nil {
		panic(err)
	}
}
