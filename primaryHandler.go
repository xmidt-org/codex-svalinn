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
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/go-kit/kit/log"
	"github.com/xmidt-org/webpa-common/logging"
	"github.com/xmidt-org/wrp-go/wrp"
)

type parser interface {
	Parse(wrp.Message) error
}

type App struct {
	parser       parser
	logger       log.Logger
	token        string
	secretGetter secretGetter
}

func (app *App) handleWebhook(writer http.ResponseWriter, req *http.Request) {
	var message wrp.Message
	msgBytes, err := ioutil.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		logging.Error(app.logger).Log(logging.MessageKey(), "Could not read request body", logging.ErrorKey(), err.Error())
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	// verify this is valid from caduceus
	encodedSecret := req.Header.Get("X-Webpa-Signature")
	trimedSecret := strings.TrimPrefix(encodedSecret, "sha1=")
	secretGiven, err := hex.DecodeString(trimedSecret)
	if err != nil {
		logging.Error(app.logger).Log(logging.MessageKey(), "Could not decode signature", logging.ErrorKey(), err.Error())
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	secret, err := app.secretGetter.GetSecret()
	if err != nil {
		logging.Error(app.logger).Log(logging.MessageKey(), "Could not get secret", logging.ErrorKey(), err.Error())
		writer.WriteHeader(http.StatusInternalServerError)
	}
	h := hmac.New(sha1.New, []byte(secret))
	h.Write(msgBytes)
	sig := h.Sum(nil)
	if !hmac.Equal(sig, secretGiven) {
		logging.Error(app.logger).Log(logging.MessageKey(), "Invalid secret")
		writer.WriteHeader(http.StatusForbidden)
		return
	}

	err = wrp.NewDecoderBytes(msgBytes, wrp.Msgpack).Decode(&message)
	if err != nil {
		logging.Error(app.logger).Log(logging.MessageKey(), "Could not decode request body", logging.ErrorKey(), err.Error())
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	logging.Debug(app.logger).Log(logging.MessageKey(), "message info", "messageType", message.Type, "fullMsg", message)
	err = app.parser.Parse(message)
	if err != nil {
		logging.Warn(app.logger).Log(logging.ErrorKey(), err.Error())
		writer.WriteHeader(http.StatusTooManyRequests)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}
