/**
 * Copyright 2019 Comcast Cable Communications Management, LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"fmt"
	"github.com/Comcast/codex/db"
	"github.com/Comcast/webpa-common/concurrent"
	"github.com/Comcast/webpa-common/logging"
	"github.com/goph/emperror"
	"github.com/gorilla/mux"
	"github.com/justinas/alice"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	//	"github.com/Comcast/webpa-common/secure/handler"
	"github.com/Comcast/webpa-common/server"
	"github.com/Comcast/webpa-common/wrp"
	"os"
	"os/signal"
	"time"
)

const (
	applicationName, apiBase = "svalinn", "/api/v1"
	DEFAULT_KEY_ID           = "current"
)

type SvalinnConfig struct {
	Endpoint            string
	QueueSize           int
	StateLimitPerDevice int
	Db                  db.DbConnection
}

func svalinn(arguments []string) int {
	start := time.Now()

	var (
		f, v                                = pflag.NewFlagSet(applicationName, pflag.ContinueOnError), viper.New()
		logger, metricsRegistry, codex, err = server.Initialize(applicationName, arguments, f, v)
	)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to initialize viper: %s\n", err.Error())
		return 1
	}
	logging.Info(logger).Log(logging.MessageKey(), "Successfully loaded config file", "configurationFile", v.ConfigFileUsed())

	/*validator, err := server.GetValidator(v, DEFAULT_KEY_ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validator error: %v\n", err)
		return 1
	}*/

	config := new(SvalinnConfig)

	v.UnmarshalKey("config", config)

	requestQueue := make(chan wrp.Message, config.QueueSize)
	pruneQueue := make(chan string, config.QueueSize)

	/*authHandler := handler.AuthorizationHandler{
		HeaderName:          "Authorization",
		ForbiddenStatusCode: 403,
		Validator:           validator,
		Logger:              logger,
	}*/

	dbConn := config.Db

	err = dbConn.Initialize()
	if err != nil {
		logging.Error(logger, emperror.Context(err)...).Log(logging.MessageKey(), "Failed to initialize database connection",
			logging.ErrorKey(), err.Error())
		fmt.Fprintf(os.Stderr, "Database Initialize Failed: %#v\n", err)
		return 2
	}

	webhookConfig := &Webhook{
		Logger: logger,
		URL:    codex.Server + apiBase + config.Endpoint,
	}
	v.UnmarshalKey("webhook", webhookConfig)
	go func() {
		if webhookConfig.RegistrationInterval > 0 {
			err := webhookConfig.Register()
			if err != nil {
				logging.Error(logger, emperror.Context(err)...).Log(logging.MessageKey(), "Failed to register webhook",
					logging.ErrorKey(), err.Error())
			} else {
				logging.Info(logger).Log(logging.MessageKey(), "Successfully registered webhook")
			}
			hookagain := time.NewTicker(webhookConfig.RegistrationInterval)
			for range hookagain.C {
				err := webhookConfig.Register()
				if err != nil {
					logging.Error(logger, emperror.Context(err)...).Log(logging.MessageKey(), "Failed to register webhook",
						logging.ErrorKey(), err.Error())
				} else {
					logging.Info(logger).Log(logging.MessageKey(), "Successfully registered webhook")
				}
			}
		}
	}()

	svalinnHandler := alice.New()
	// TODO: add authentication back
	//svalinnHandler := alice.New(authHandler.Decorate)
	router := mux.NewRouter()
	// MARK: Actual server logic

	app := &App{
		logger:       logger,
		requestQueue: requestQueue,
	}

	// TODO: Fix Caduces acutal register
	router.Handle(apiBase+config.Endpoint, svalinnHandler.ThenFunc(app.handleWebhook))

	go handleRequests(requestQueue, pruneQueue, logger, dbConn)
	go handlePruning(pruneQueue, logger, dbConn, config.StateLimitPerDevice)

	// MARK: Starting the server
	_, runnable, done := codex.Prepare(logger, nil, metricsRegistry, router)

	waitGroup, shutdown, err := concurrent.Execute(runnable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to start device manager: %s\n", err)
		return 1
	}

	logging.Info(logger).Log(logging.MessageKey(), fmt.Sprintf("%s is up and running!", applicationName), "elapsedTime", time.Since(start))
	signals := make(chan os.Signal, 10)
	signal.Notify(signals)
	for exit := false; !exit; {
		select {
		case s := <-signals:
			if s != os.Kill && s != os.Interrupt {
				logging.Info(logger).Log(logging.MessageKey(), "ignoring signal", "signal", s)
			} else {
				logging.Error(logger).Log(logging.MessageKey(), "exiting due to signal", "signal", s)
				exit = true
			}
		case <-done:
			logging.Error(logger).Log(logging.MessageKey(), "one or more servers exited")
			exit = true
		}
	}

	close(shutdown)
	waitGroup.Wait()
	return 0
}

func main() {
	os.Exit(svalinn(os.Args))
}
