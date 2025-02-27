package gorush

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/crypto/acme/autocert"
)

func init() {
	// Support metrics
	m := NewMetrics()
	prometheus.MustRegister(m)
}

func abortWithError(c *gin.Context, code int, message string) {
	c.AbortWithStatusJSON(code, gin.H{
		"code":    code,
		"message": message,
	})
}

func rootHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"text": "Welcome to notification server.",
	})
}

func heartbeatHandler(c *gin.Context) {
	c.AbortWithStatus(http.StatusOK)
}

func versionHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"source":  "https://github.com/appleboy/gorush",
		"version": GetVersion(),
	})
}

func pushHandler(c *gin.Context) {
	var form RequestPush
	var msg string

	if err := c.ShouldBindWith(&form, binding.JSON); err != nil {
		msg = "Missing notifications field."
		LogAccess.Debug(err)
		abortWithError(c, http.StatusBadRequest, msg)
		return
	}

	if len(form.Notifications) == 0 {
		msg = "Notifications field is empty."
		LogAccess.Debug(msg)
		abortWithError(c, http.StatusBadRequest, msg)
		return
	}

	if int64(len(form.Notifications)) > PushConf.Core.MaxNotification {
		msg = fmt.Sprintf("Number of notifications(%d) over limit(%d)", len(form.Notifications), PushConf.Core.MaxNotification)
		LogAccess.Debug(msg)
		abortWithError(c, http.StatusBadRequest, msg)
		return
	}

	counts, logs := queueNotification(form)

	c.JSON(http.StatusOK, gin.H{
		"success": "ok",
		"counts":  counts,
		"logs":    logs,
	})
}

func configHandler(c *gin.Context) {
	c.YAML(http.StatusCreated, PushConf)
}

func metricsHandler(c *gin.Context) {
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
}

func autoTLSServer() *http.Server {
	m := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(PushConf.Core.AutoTLS.Host),
		Cache:      autocert.DirCache(PushConf.Core.AutoTLS.Folder),
	}

	return &http.Server{
		Addr:      ":https",
		TLSConfig: &tls.Config{GetCertificate: m.GetCertificate},
		Handler:   routerEngine(),
	}
}

func routerEngine() *gin.Engine {
	// set server mode
	gin.SetMode(PushConf.Core.Mode)

	r := gin.New()

	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	r.Use(VersionMiddleware())
	r.Use(LogMiddleware())
	r.Use(StatMiddleware())

	var api *gin.RouterGroup
	var metrics *gin.RouterGroup

	// enable basic auth
	if PushConf.Auth.Enabled {
		basicAuth := gin.BasicAuth(gin.Accounts{
			PushConf.Auth.Username: PushConf.Auth.Password,
		})
		api = r.Group("/api", basicAuth)
		metrics = r.Group(PushConf.API.MetricURI, basicAuth)
	} else {
		api = r.Group("/api")
		metrics = r.Group(PushConf.API.MetricURI)
	}
	api.GET(PushConf.API.StatGoURI, appStatusHandler)
	api.GET(PushConf.API.StatAppURI, appStatusHandler)
	api.GET(PushConf.API.ConfigURI, configHandler)
	api.GET(PushConf.API.SysStatURI, sysStatsHandler)
	api.POST(PushConf.API.PushURI, pushHandler)
	metrics.GET("", metricsHandler)
	api.GET("/version", versionHandler)
	api.GET("/", rootHandler)
	r.GET(PushConf.API.HealthURI, heartbeatHandler)

	return r
}

// RunHTTPServer provide run http or https protocol.
func RunHTTPServer() (err error) {
	if !PushConf.Core.Enabled {
		LogAccess.Debug("httpd server is disabled.")
		return nil
	}

	server := &http.Server{
		Addr:    PushConf.Core.Address + ":" + PushConf.Core.Port,
		Handler: routerEngine(),
	}

	LogAccess.Debug("HTTPD server is running on " + PushConf.Core.Port + " port.")
	if PushConf.Core.AutoTLS.Enabled {
		return startServer(autoTLSServer())
	} else if PushConf.Core.SSL {
		config := &tls.Config{
			MinVersion: tls.VersionTLS10,
		}

		if config.NextProtos == nil {
			config.NextProtos = []string{"http/1.1"}
		}

		config.Certificates = make([]tls.Certificate, 1)
		if PushConf.Core.CertPath != "" && PushConf.Core.KeyPath != "" {
			config.Certificates[0], err = tls.LoadX509KeyPair(PushConf.Core.CertPath, PushConf.Core.KeyPath)
			if err != nil {
				LogError.Error("Failed to load https cert file: ", err)
				return err
			}
		} else if PushConf.Core.CertBase64 != "" && PushConf.Core.KeyBase64 != "" {
			cert, err := base64.StdEncoding.DecodeString(PushConf.Core.CertBase64)
			if err != nil {
				LogError.Error("base64 decode error:", err.Error())
				return err
			}
			key, err := base64.StdEncoding.DecodeString(PushConf.Core.KeyBase64)
			if err != nil {
				LogError.Error("base64 decode error:", err.Error())
				return err
			}
			if config.Certificates[0], err = tls.X509KeyPair(cert, key); err != nil {
				LogError.Error("tls key pair error:", err.Error())
				return err
			}
		} else {
			return errors.New("missing https cert config")
		}

		server.TLSConfig = config
	}

	return startServer(server)
}

func startServer(s *http.Server) error {
	if s.TLSConfig == nil {
		return s.ListenAndServe()
	}
	return s.ListenAndServeTLS("", "")
}
