// Licensed to Apache Software Foundation (ASF) under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Apache Software Foundation (ASF) licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package http

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.uber.org/multierr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	database_v1 "github.com/apache/skywalking-banyandb/api/proto/banyandb/database/v1"
	measure_v1 "github.com/apache/skywalking-banyandb/api/proto/banyandb/measure/v1"
	property_v1 "github.com/apache/skywalking-banyandb/api/proto/banyandb/property/v1"
	stream_v1 "github.com/apache/skywalking-banyandb/api/proto/banyandb/stream/v1"
	"github.com/apache/skywalking-banyandb/pkg/logger"
	"github.com/apache/skywalking-banyandb/pkg/run"
	"github.com/apache/skywalking-banyandb/ui"
)

type ServiceRepo interface {
	run.Config
	run.Service
}

var _ ServiceRepo = (*service)(nil)

func NewService() ServiceRepo {
	return &service{
		stopCh: make(chan struct{}),
	}
}

type service struct {
	listenAddr   string
	grpcAddr     string
	mux          *chi.Mux
	stopCh       chan struct{}
	clientCloser context.CancelFunc
	l            *logger.Logger

	srv *http.Server
}

func (p *service) FlagSet() *run.FlagSet {
	flagSet := run.NewFlagSet("")
	flagSet.StringVar(&p.listenAddr, "http-addr", ":17913", "listen addr for http")
	flagSet.StringVar(&p.grpcAddr, "grpc-addr", "localhost:17912", "the grpc addr")
	return flagSet
}

func (p *service) Validate() error {
	return nil
}

func (p *service) Name() string {
	return "liaison-http"
}

func (p *service) PreRun() error {
	p.l = logger.GetLogger(p.Name())
	p.mux = chi.NewRouter()

	fSys, err := fs.Sub(ui.DistContent, "dist")
	if err != nil {
		return err
	}
	httpFS := http.FS(fSys)
	fileServer := http.FileServer(http.FS(fSys))
	serveIndex := serveFileContents("index.html", httpFS)
	p.mux.Mount("/", intercept404(fileServer, serveIndex))
	p.srv = &http.Server{
		Addr:    p.listenAddr,
		Handler: p.mux,
	}
	return nil
}

func (p *service) Serve() run.StopNotify {
	var ctx context.Context
	ctx, p.clientCloser = context.WithCancel(context.Background())
	opts := []grpc.DialOption{
		// TODO: add TLS
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	client, err := newHealthCheckClient(ctx, p.l, p.grpcAddr, opts)
	if err != nil {
		p.l.Error().Err(err).Msg("Failed to health check client")
		close(p.stopCh)
		return p.stopCh
	}
	gwMux := runtime.NewServeMux(runtime.WithHealthzEndpoint(client))
	err = multierr.Combine(
		database_v1.RegisterStreamRegistryServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		database_v1.RegisterMeasureRegistryServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		database_v1.RegisterIndexRuleRegistryServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		database_v1.RegisterIndexRuleBindingRegistryServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		database_v1.RegisterGroupRegistryServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		stream_v1.RegisterStreamServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		measure_v1.RegisterMeasureServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
		property_v1.RegisterPropertyServiceHandlerFromEndpoint(ctx, gwMux, p.grpcAddr, opts),
	)
	if err != nil {
		p.l.Error().Err(err).Msg("Failed to register endpoints")
		close(p.stopCh)
		return p.stopCh
	}
	p.mux.Mount("/api", http.StripPrefix("/api", gwMux))
	go func() {
		p.l.Info().Str("listenAddr", p.listenAddr).Msg("Start liaison http server")
		if err := p.srv.ListenAndServe(); err != http.ErrServerClosed {
			p.l.Error().Err(err)
		}
		close(p.stopCh)
	}()
	return p.stopCh
}

func (p *service) GracefulStop() {
	if err := p.srv.Close(); err != nil {
		p.l.Error().Err(err)
	}
	p.clientCloser()
}

func intercept404(handler, on404 http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hookedWriter := &hookedResponseWriter{ResponseWriter: w}
		handler.ServeHTTP(hookedWriter, r)

		if hookedWriter.got404 {
			on404.ServeHTTP(w, r)
		}
	}
}

type hookedResponseWriter struct {
	http.ResponseWriter
	got404 bool
}

func (hrw *hookedResponseWriter) WriteHeader(status int) {
	if status == http.StatusNotFound {
		hrw.got404 = true
	} else {
		hrw.ResponseWriter.WriteHeader(status)
	}
}

func (hrw *hookedResponseWriter) Write(p []byte) (int, error) {
	if hrw.got404 {
		return len(p), nil
	}

	return hrw.ResponseWriter.Write(p)
}

func serveFileContents(file string, files http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "404 not found")

			return
		}
		index, err := files.Open(file)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "%s not found", file)

			return
		}
		fi, err := index.Stat()
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "%s not found", file)

			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), index)
	}
}
