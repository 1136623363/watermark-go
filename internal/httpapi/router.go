package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/observability"
)

type RouterOptions struct {
	Client      ClientHandlers
	Parse       ParseHandlers
	ParseTasks  ParseTaskHandlers
	Download    DownloadHandlers
	Admin       AdminHandlers
	Performance *observability.PerformanceCollector
	Logger      EventLogger
}

type ServerOptions struct {
	Addr              string
	Handler           http.Handler
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type Server struct {
	server *http.Server
	done   chan error
	once   sync.Once
}

func Router(options RouterOptions) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(RequestIDMiddleware(), CORSMiddleware(), RequestLogMiddleware(options.Logger))
	router.GET("/healthz", health)

	router.POST("/api/client/session", options.Client.ClientSession)
	if options.Client.Auth != nil || options.Client.Parse != nil {
		router.POST("/api/parse", options.Client.ParseAuthenticated)
	} else {
		router.POST("/api/parse", options.Parse.Parse)
	}
	router.GET("/api/hybrid/video_data", options.Parse.HybridVideoData)
	router.GET("/video/share/url/parse", options.Parse.LegacyShareURLParse)
	router.GET("/video/id/parse", options.Parse.LegacyIDParse)
	router.GET("/api/parse/cache/:id", options.Parse.ParseCache)
	router.GET("/api/v1/parse", options.Parse.V1Parse)
	router.GET("/api/v1/parse/:source/:video_id", options.Parse.V1ParseID)
	options.ParseTasks.Register(router)
	options.Download.Register(router)
	router.GET("/api/m3u8/merge", options.Download.CreateM3U8)
	options.Admin.Register(router)
	performance := options.Performance
	if performance == nil {
		performance = observability.NewPerformanceCollector(observability.PerformanceOptions{})
	}
	router.POST("/api/client/performance", gin.WrapF(performance.ServeHTTP))
	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: gin.H{"status": "ok"}})
}

func NewServer(options ServerOptions) *Server {
	handler := options.Handler
	if handler == nil {
		handler = Router(RouterOptions{})
	}
	readHeaderTimeout := options.ReadHeaderTimeout
	if readHeaderTimeout <= 0 {
		readHeaderTimeout = 10 * time.Second
	}
	readTimeout := options.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 20 * time.Second
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 40 * time.Second
	}
	idleTimeout := options.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Second
	}
	addr := strings.TrimSpace(options.Addr)
	if addr == "" {
		addr = ":5001"
	}
	return &Server{
		server: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
		},
		done: make(chan error, 1),
	}
}

func NewServerFromConfig(cfg config.Config, handler http.Handler) *Server {
	port := strings.TrimSpace(cfg.HTTP.Port)
	if port == "" {
		port = "5001"
	}
	return NewServer(ServerOptions{Addr: ":" + port, Handler: handler})
}

func (server *Server) HTTPServer() *http.Server {
	if server == nil {
		return nil
	}
	return server.server
}

func (server *Server) Start(ctx context.Context) error {
	if server == nil || server.server == nil {
		return errors.New("nil http server")
	}
	listener, err := net.Listen("tcp", server.server.Addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.server.Shutdown(shutdownCtx)
	}()
	go func() {
		err := server.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		server.done <- err
		close(server.done)
	}()
	return nil
}

func (server *Server) Done() <-chan error {
	if server == nil {
		done := make(chan error)
		close(done)
		return done
	}
	return server.done
}

func (server *Server) Stop(ctx context.Context) error {
	if server == nil || server.server == nil {
		return nil
	}
	var err error
	server.once.Do(func() {
		err = server.server.Shutdown(ctx)
	})
	return err
}
