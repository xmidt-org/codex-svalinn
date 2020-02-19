package requestParser

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/metrics/provider"

	"github.com/xmidt-org/codex-db/blacklist"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	db "github.com/xmidt-org/codex-db"
	"github.com/xmidt-org/svalinn/rules"
	"github.com/xmidt-org/voynicrypto"
	"github.com/xmidt-org/webpa-common/logging"
	"github.com/xmidt-org/webpa-common/semaphore"
	"github.com/xmidt-org/webpa-common/xmetrics/xmetricstest"
	"github.com/xmidt-org/wrp-go/v2"
)

var (
	goodEvent = wrp.Message{
		Source:          "test source",
		Destination:     "/test/",
		Type:            wrp.SimpleEventMessageType,
		PartnerIDs:      []string{"test1", "test2"},
		TransactionUUID: "transaction test uuid",
		Payload:         []byte(`{"ts":"2019-02-13T21:19:02.614191735Z"}`),
		Metadata:        map[string]string{"testkey": "testvalue"},
	}
)

func TestNewRequestParser(t *testing.T) {
	goodEncrypter := new(mockEncrypter)
	goodInserter := new(mockInserter)
	goodBlacklist := new(mockBlacklist)
	goodRegistry := xmetricstest.NewProvider(nil, Metrics)
	goodMeasures := NewMeasures(goodRegistry)
	goodConfig := Config{
		QueueSize:       1000,
		MetadataMaxSize: 100000,
		PayloadMaxSize:  1000000,
		MaxWorkers:      5000,
		DefaultTTL:      5 * time.Hour,
	}
	tests := []struct {
		description           string
		encrypter             voynicrypto.Encrypt
		blacklist             blacklist.List
		inserter              inserter
		config                Config
		logger                log.Logger
		registry              provider.Provider
		expectedRequestParser *RequestParser
		expectedErr           error
	}{
		{
			description: "Success",
			encrypter:   goodEncrypter,
			blacklist:   goodBlacklist,
			inserter:    goodInserter,
			registry:    goodRegistry,
			config:      goodConfig,
			logger:      log.NewJSONLogger(os.Stdout),
			expectedRequestParser: &RequestParser{
				encrypter: goodEncrypter,
				blacklist: goodBlacklist,
				inserter:  goodInserter,
				measures:  goodMeasures,
				config:    goodConfig,
				logger:    log.NewJSONLogger(os.Stdout),
			},
		},
		{
			description: "Success With Defaults",
			encrypter:   goodEncrypter,
			blacklist:   goodBlacklist,
			inserter:    goodInserter,
			registry:    goodRegistry,
			config: Config{
				MetadataMaxSize: -5,
				PayloadMaxSize:  -5,
			},
			expectedRequestParser: &RequestParser{
				encrypter: goodEncrypter,
				blacklist: goodBlacklist,
				inserter:  goodInserter,
				measures:  goodMeasures,
				config: Config{
					QueueSize:  defaultMinQueueSize,
					DefaultTTL: defaultTTL,
					MaxWorkers: minMaxWorkers,
				},
				logger: defaultLogger,
			},
		},
		{
			description: "No Encrypter Error",
			expectedErr: errors.New("no encrypter"),
		},
		{
			description: "No Blacklist Error",
			encrypter:   goodEncrypter,
			expectedErr: errors.New("no blacklist"),
		},
		{
			description: "No Inserter Error",
			encrypter:   goodEncrypter,
			blacklist:   goodBlacklist,
			expectedErr: errors.New("no inserter"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			assert := assert.New(t)
			rp, err := NewRequestParser(tc.config, tc.logger, tc.registry, tc.inserter, tc.blacklist, tc.encrypter, nil)
			if rp != nil {
				tc.expectedRequestParser.requestQueue = rp.requestQueue
				tc.expectedRequestParser.parseWorkers = rp.parseWorkers
				rp.currTime = nil
			}
			assert.Equal(tc.expectedRequestParser, rp)
			if tc.expectedErr == nil || err == nil {
				assert.Equal(tc.expectedErr, err)
			} else {
				assert.Contains(err.Error(), tc.expectedErr.Error())
			}
		})
	}
}

func TestParseRequest(t *testing.T) {
	testassert := assert.New(t)
	goodTime, err := time.Parse(time.RFC3339Nano, "2019-02-13T21:19:02.614191735Z")
	testassert.Nil(err)
	beginTime := time.Now()
	tests := []struct {
		description        string
		req                wrp.Message
		encryptErr         error
		expectEncryptCount float64
		expectParseCount   float64
		encryptCalled      bool
		blacklistCalled    bool
		insertCalled       bool
		timeExpected       bool
	}{
		{
			description:     "Success",
			req:             goodEvent,
			encryptCalled:   true,
			blacklistCalled: true,
			insertCalled:    true,
			timeExpected:    true,
		},
		{
			description:      "Empty ID Error",
			expectParseCount: 1.0,
		},
		{
			description:        "Encrypt Error",
			req:                goodEvent,
			encryptErr:         errors.New("encrypt failed"),
			expectEncryptCount: 1.0,
			encryptCalled:      true,
			blacklistCalled:    true,
			timeExpected:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			encrypter := new(mockEncrypter)
			if tc.encryptCalled {
				encrypter.On("EncryptMessage", mock.Anything).Return(tc.encryptErr)
			}

			mblacklist := new(mockBlacklist)
			if tc.blacklistCalled {
				mblacklist.On("InList", mock.Anything).Return("", false).Once()
			}

			mockInserter := new(mockInserter)
			if tc.insertCalled {
				mockInserter.On("Insert", mock.Anything).Return().Once()
			}

			mockTimeTracker := new(mockTimeTracker)
			if !tc.insertCalled {
				mockTimeTracker.On("TrackTime", mock.Anything).Once()
			}

			p := xmetricstest.NewProvider(nil, Metrics)
			m := NewMeasures(p)

			timeCalled := false
			timeFunc := func() time.Time {
				timeCalled = true
				return goodTime
			}

			handler := RequestParser{
				encrypter: encrypter,
				config: Config{
					PayloadMaxSize:  9999,
					MetadataMaxSize: 9999,
					DefaultTTL:      time.Second,
					MaxWorkers:      5,
				},
				inserter:     mockInserter,
				timeTracker:  mockTimeTracker,
				parseWorkers: semaphore.New(2),
				measures:     m,
				logger:       logging.NewTestLogger(nil, t),
				blacklist:    mblacklist,
				currTime:     timeFunc,
			}

			handler.parseWorkers.Acquire()
			handler.parseRequest(WrpWithTime{Message: tc.req, Beginning: beginTime})
			mockInserter.AssertExpectations(t)
			mblacklist.AssertExpectations(t)
			encrypter.AssertExpectations(t)
			mockTimeTracker.AssertExpectations(t)
			p.Assert(t, DroppedEventsCounter, reasonLabel, encryptFailReason)(xmetricstest.Value(tc.expectEncryptCount))
			p.Assert(t, DroppedEventsCounter, reasonLabel, parseFailReason)(xmetricstest.Value(tc.expectParseCount))
			testassert.Equal(tc.timeExpected, timeCalled)

		})
	}
}

func TestCreateRecord(t *testing.T) {
	testassert := assert.New(t)
	goodTime, err := time.Parse(time.RFC3339Nano, "2019-02-13T21:19:02.614191735Z")
	testassert.Nil(err)
	tests := []struct {
		description      string
		req              wrp.Message
		storePayload     bool
		eventType        db.EventType
		blacklistCalled  bool
		inBlacklist      bool
		timeExpected     bool
		timeToReturn     time.Time
		maxPayloadSize   int
		maxMetadataSize  int
		encryptCalled    bool
		encryptErr       error
		expectedDeviceID string
		expectedEvent    wrp.Message
		emptyRecord      bool
		expectedReason   string
		expectedErr      error
	}{
		{
			description:      "Success",
			req:              goodEvent,
			expectedDeviceID: "test",
			expectedEvent:    goodEvent,
			storePayload:     true,
			eventType:        db.State,
			blacklistCalled:  true,
			timeExpected:     true,
			timeToReturn:     goodTime,
			encryptCalled:    true,
			maxMetadataSize:  500,
			maxPayloadSize:   500,
		},
		{
			description: "Success Uppercase Device ID",
			req: wrp.Message{
				Source:          goodEvent.Source,
				Destination:     strings.ToUpper(goodEvent.Destination),
				PartnerIDs:      goodEvent.PartnerIDs,
				TransactionUUID: goodEvent.TransactionUUID,
				Type:            goodEvent.Type,
				Payload:         goodEvent.Payload,
				Metadata:        goodEvent.Metadata,
			},
			expectedDeviceID: "test",
			expectedEvent: wrp.Message{
				Source:          goodEvent.Source,
				Destination:     strings.ToUpper(goodEvent.Destination),
				PartnerIDs:      goodEvent.PartnerIDs,
				TransactionUUID: goodEvent.TransactionUUID,
				Type:            goodEvent.Type,
				Payload:         goodEvent.Payload,
				Metadata:        goodEvent.Metadata,
			},
			eventType:       db.State,
			storePayload:    true,
			blacklistCalled: true,
			timeExpected:    true,
			timeToReturn:    goodTime,
			encryptCalled:   true,
			maxMetadataSize: 500,
			maxPayloadSize:  500,
		},
		{
			description: "Success Source Device and No Birthdate",
			req: wrp.Message{
				Source:          goodEvent.Source,
				PartnerIDs:      goodEvent.PartnerIDs,
				TransactionUUID: goodEvent.TransactionUUID,
				Type:            goodEvent.Type,
				Metadata:        goodEvent.Metadata,
			},
			expectedDeviceID: goodEvent.Source,
			expectedEvent: wrp.Message{
				Source:          goodEvent.Source,
				PartnerIDs:      goodEvent.PartnerIDs,
				TransactionUUID: goodEvent.TransactionUUID,
				Type:            goodEvent.Type,
				Metadata:        goodEvent.Metadata,
			},
			storePayload:    true,
			blacklistCalled: true,
			timeExpected:    true,
			timeToReturn:    goodTime,
			encryptCalled:   true,
			maxMetadataSize: 500,
			maxPayloadSize:  500,
		},
		{
			description:      "Success Empty Metadata/Payload",
			req:              goodEvent,
			expectedDeviceID: "test",
			expectedEvent: wrp.Message{
				Source:          goodEvent.Source,
				Destination:     goodEvent.Destination,
				PartnerIDs:      goodEvent.PartnerIDs,
				TransactionUUID: goodEvent.TransactionUUID,
				Type:            goodEvent.Type,
				Payload:         nil,
				Metadata:        map[string]string{"error": "metadata provided exceeds size limit - too big to store"},
			},
			blacklistCalled: true,
			timeExpected:    true,
			timeToReturn:    goodTime,
			encryptCalled:   true,
			eventType:       db.State,
		},
		{
			description: "Empty Dest ID Error",
			req: wrp.Message{
				Destination: "//",
			},
			eventType:      db.State,
			emptyRecord:    true,
			expectedReason: parseFailReason,
			expectedErr:    errEmptyID,
		},
		{
			description:    "Empty Source ID Error",
			req:            wrp.Message{},
			emptyRecord:    true,
			expectedReason: parseFailReason,
			expectedErr:    errEmptyID,
		},
		{
			description: "Blacklist Error",
			req: wrp.Message{
				Source: " ",
			},
			emptyRecord:     true,
			inBlacklist:     true,
			blacklistCalled: true,
			expectedReason:  blackListReason,
			expectedErr:     errBlacklist,
		},
		{
			description: "Unexpected WRP Type Error",
			req: wrp.Message{
				Destination: "/device/",
				Type:        5,
			},
			eventType:       db.State,
			emptyRecord:     true,
			blacklistCalled: true,
			expectedReason:  parseFailReason,
			expectedErr:     errUnexpectedWRPType,
		},
		{
			description:     "Future Birthdate Error",
			req:             goodEvent,
			eventType:       db.State,
			emptyRecord:     true,
			blacklistCalled: true,
			timeExpected:    true,
			timeToReturn:    goodTime.Add(-5 * time.Hour),
			expectedReason:  invalidBirthdateReason,
			expectedErr:     errFutureBirthdate,
		},
		{
			description:     "Past Deathdate Error",
			req:             goodEvent,
			eventType:       db.State,
			emptyRecord:     true,
			blacklistCalled: true,
			timeExpected:    true,
			timeToReturn:    goodTime.Add(5 * time.Hour),
			expectedReason:  expiredReason,
			expectedErr:     errExpired,
		},
		{
			description:     "Encrypt Error",
			req:             goodEvent,
			encryptErr:      errors.New("encrypt failed"),
			emptyRecord:     true,
			blacklistCalled: true,
			encryptCalled:   true,
			timeExpected:    true,
			timeToReturn:    goodTime,
			expectedReason:  encryptFailReason,
			expectedErr:     errors.New("failed to encrypt message"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			assert := assert.New(t)
			var buffer bytes.Buffer
			wrpEncoder := wrp.NewEncoder(&buffer, wrp.Msgpack)
			err := wrpEncoder.Encode(&tc.expectedEvent)
			assert.Nil(err)
			var expectedRecord db.Record
			if !tc.emptyRecord {
				expectedRecord = db.Record{
					Type:      tc.eventType,
					DeviceID:  tc.expectedDeviceID,
					BirthDate: goodTime.UnixNano(),
					DeathDate: goodTime.Add(time.Second).UnixNano(),
					Data:      buffer.Bytes(),
					Nonce:     []byte{},
					Alg:       string(voynicrypto.None),
					KID:       "none",
				}
			}
			r, err := rules.NewRules([]rules.RuleConfig{
				{
					Regex:        ".*",
					StorePayload: tc.storePayload,
					RuleTTL:      time.Second,
				},
			})
			assert.Nil(err)
			rule, err := r.FindRule(" ")
			assert.Nil(err)
			encrypter := new(mockEncrypter)
			if tc.encryptCalled {
				encrypter.On("EncryptMessage", mock.Anything).Return(tc.encryptErr).Once()
			}
			mblacklist := new(mockBlacklist)
			if tc.blacklistCalled {
				mblacklist.On("InList", mock.Anything).Return("", tc.inBlacklist).Once()
			}

			timeCalled := false
			timeFunc := func() time.Time {
				timeCalled = true
				return tc.timeToReturn
			}

			handler := RequestParser{
				encrypter: encrypter,
				config: Config{
					PayloadMaxSize:  tc.maxPayloadSize,
					MetadataMaxSize: tc.maxMetadataSize,
				},
				blacklist: mblacklist,
				currTime:  timeFunc,
			}
			record, reason, err := handler.createRecord(tc.req, rule, tc.eventType)
			encrypter.AssertExpectations(t)
			mblacklist.AssertExpectations(t)
			assert.Equal(expectedRecord, record)
			assert.Equal(tc.expectedReason, reason)
			assert.Equal(tc.timeExpected, timeCalled)
			if tc.expectedErr == nil || err == nil {
				assert.Equal(tc.expectedErr, err)
			} else {
				assert.Contains(err.Error(), tc.expectedErr.Error())
			}
		})
	}
}

func TestGetBirthDate(t *testing.T) {
	testassert := assert.New(t)
	goodTime, err := time.Parse(time.RFC3339Nano, "2019-02-13T21:19:02.614191735Z")
	testassert.Nil(err)
	tests := []struct {
		description   string
		payload       []byte
		expectedTime  time.Time
		expectedFound bool
	}{
		{
			description:   "Success",
			payload:       goodEvent.Payload,
			expectedTime:  goodTime,
			expectedFound: true,
		},
		{
			description: "Unmarshal Payload Error",
			payload:     []byte("test"),
		},
		{
			description: "Empty Payload String Error",
			payload:     []byte(``),
		},
		{
			description: "Non-String Timestamp Error",
			payload:     []byte(`{"ts":5}`),
		},
		{
			description: "Parse Timestamp Error",
			payload:     []byte(`{"ts":"2345"}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			assert := assert.New(t)
			time, found := getBirthDate(tc.payload)
			assert.Equal(time, tc.expectedTime)
			assert.Equal(found, tc.expectedFound)
		})
	}
}
