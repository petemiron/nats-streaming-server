package server

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/go-stan"
	"github.com/nats-io/go-stan/pb"
	"github.com/nats-io/nats"
	"github.com/nats-io/stan-server/stores"

	natsd "github.com/nats-io/gnatsd/server"
	"sync"
)

const (
	// CAUTION! Tests will remove the directory and all its content,
	// so pick a directory where there is nothing.
	defaultDataStore = "../server_data"
)

const (
	clusterName = DefaultClusterID
	clientName  = "me"
)

// So that we can pass tests and benchmarks...
type tLogger interface {
	Fatalf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

func stackFatalf(t tLogger, f string, args ...interface{}) {
	lines := make([]string, 0, 32)
	msg := fmt.Sprintf(f, args...)
	lines = append(lines, msg)

	// Generate the Stack of callers:
	for i := 1; true; i++ {
		_, file, line, ok := runtime.Caller(i)
		if ok == false {
			break
		}
		msg := fmt.Sprintf("%d - %s:%d", i, file, line)
		lines = append(lines, msg)
	}

	t.Fatalf("%s", strings.Join(lines, "\n"))
}

func NewDefaultConnection(t tLogger) stan.Conn {
	sc, err := stan.Connect(clusterName, clientName)
	if err != nil {
		stackFatalf(t, "Expected to connect correctly, got err %v", err)
	}
	return sc
}

func cleanupDatastore(t *testing.T, dir string) {
	if err := os.RemoveAll(dir); err != nil {
		stackFatalf(t, "Error cleanup datastore: %v", err)
	}
}

// Dumb wait program to sync on callbacks, etc... Will timeout
func Wait(ch chan bool) error {
	return WaitTime(ch, 5*time.Second)
}

func WaitTime(ch chan bool, timeout time.Duration) error {
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
	}
	return errors.New("timeout")
}

// RunServerWithDebugTrace is a helper to assist debugging
func RunServerWithDebugTrace(opts *Options, enableDebug, enableTrace bool) *StanServer {
	natsdOpts := &natsd.Options{}

	natsdOpts.Debug = enableDebug
	natsdOpts.Trace = enableTrace
	natsdOpts.NoLog = false

	EnableDefaultLogger(natsdOpts)

	return RunServerWithOpts(opts, natsdOpts)
}

func TestRunServer(t *testing.T) {
	// Test passing nil options
	s := RunServerWithOpts(nil, nil)
	s.Shutdown()

	// Test passing stan options, nil nats options
	opts := DefaultOptions
	s = RunServerWithOpts(&opts, nil)
	defer s.Shutdown()
	clusterID := s.ClusterID()

	if clusterID != clusterName {
		t.Fatalf("Expected cluster ID of %s, found %s\n", clusterName, clusterID)
	}
	s.Shutdown()

	// Test passing nil stan options, some nats options
	nOpts := &natsd.Options{}
	s = RunServerWithOpts(nil, nOpts)
	defer s.Shutdown()
}

func TestDoubleShutdown(t *testing.T) {
	s := RunServer(clusterName)
	s.Shutdown()

	ch := make(chan bool)

	go func() {
		s.Shutdown()
		ch <- true
	}()

	if err := Wait(ch); err != nil {
		t.Fatal("Second shutdown blocked")
	}
}

type response interface {
	Unmarshal([]byte) error
}

func checkServerResponse(nc *nats.Conn, subj string, expectedError error, r response) error {
	resp, err := nc.Request(subj, []byte("dummy"), time.Second)
	if err != nil {
		return fmt.Errorf("Unexpected error on publishing request: %v", err)
	}
	if err := r.Unmarshal(resp.Data); err != nil {
		return fmt.Errorf("Unexpected response object: %v", err)
	}
	// All our protos have the Error field.
	v := reflect.Indirect(reflect.ValueOf(r))
	f := v.FieldByName("Error")
	if !f.IsValid() {
		return fmt.Errorf("Field Error not found in the response: %v", f)
	}
	connErr := f.String()
	if connErr != expectedError.Error() {
		return fmt.Errorf("Expected response to be %q, got %q", expectedError.Error(), connErr)
	}
	return nil
}

func TestInvalidRequests(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	// Use a bare NATS connection to send incorrect requests
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	// Send a dummy message on the STAN connect subject
	// Get the connect subject
	connSubj := fmt.Sprintf("%s.%s", s.opts.DiscoverPrefix, clusterName)
	if err := checkServerResponse(nc, connSubj, ErrInvalidConnReq,
		&pb.ConnectResponse{}); err != nil {
		t.Fatalf("%v", err)
	}

	// Send a dummy message on the STAN publish subject
	if err := checkServerResponse(nc, s.info.Publish+".foo", ErrInvalidPubReq,
		&pb.PubAck{}); err != nil {
		t.Fatalf("%v", err)
	}

	// Send a dummy message on the STAN subscription init subject
	if err := checkServerResponse(nc, s.info.Subscribe, ErrInvalidSubReq,
		&pb.SubscriptionResponse{}); err != nil {
		t.Fatalf("%v", err)
	}

	// Send a dummy message on the STAN subscription unsub subject
	if err := checkServerResponse(nc, s.info.Unsubscribe, ErrInvalidUnsubReq,
		&pb.SubscriptionResponse{}); err != nil {
		t.Fatalf("%v", err)
	}

	// Send a dummy message on the STAN close subject
	if err := checkServerResponse(nc, s.info.Close, ErrInvalidCloseReq,
		&pb.CloseResponse{}); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestClientIDIsValid(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	// Use a bare NATS connection to send incorrect requests
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	// Get the connect subject
	connSubj := fmt.Sprintf("%s.%s", s.opts.DiscoverPrefix, clusterName)

	invalidClientIDs := []string{"", "id with spaces", "id:with:columns",
		"id,with,commas", "id.with.dots", "id with spaces, commas and: columns and dots.",
		"idWithLotsOfNotAllowedCharacters!@#$%^&*()"}

	for _, cID := range invalidClientIDs {
		req := &pb.ConnectRequest{ClientID: cID, HeartbeatInbox: "hbInbox"}
		b, _ := req.Marshal()

		resp, err := nc.Request(connSubj, b, time.Second)
		if err != nil {
			t.Fatalf("Unexpected error on publishing request: %v", err)
		}
		r := &pb.ConnectResponse{}
		err = r.Unmarshal(resp.Data)
		if err != nil {
			t.Fatalf("Unexpected response object: %v", err)
		}
		if r.Error == "" {
			t.Fatal("Expected error, got none")
		}
	}

	validClientIDs := []string{"id", "id_with_underscores", "id-with-hypens"}

	for _, cID := range validClientIDs {
		req := &pb.ConnectRequest{ClientID: cID, HeartbeatInbox: "hbInbox"}
		b, _ := req.Marshal()

		resp, err := nc.Request(connSubj, b, time.Second)
		if err != nil {
			t.Fatalf("Unexpected error on publishing request: %v", err)
		}
		r := &pb.ConnectResponse{}
		err = r.Unmarshal(resp.Data)
		if err != nil {
			t.Fatalf("Unexpected response object: %v", err)
		}
		if r.Error != "" {
			t.Fatalf("Unexpected response error: %v", r.Error)
		}
	}
}

func sendInvalidSubRequest(s *StanServer, nc *nats.Conn, req *pb.SubscriptionRequest) error {
	b, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("Error during marshal: %v", err)
	}
	rep, err := nc.Request(s.info.Subscribe, b, time.Second)
	if err != nil {
		return fmt.Errorf("Unexpected error: %v", err)
	}
	// Check response
	subRep := &pb.SubscriptionResponse{}
	subRep.Unmarshal(rep.Data)

	// Expect error
	if subRep.Error == "" {
		return fmt.Errorf("Expected error, got none")
	}
	return nil
}

func TestInvalidSubRequest(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	// Use a bare NATS connection to send incorrect requests
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	// Create empty request
	req := &pb.SubscriptionRequest{}

	// Send this empty request
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Add a valid ackWait
	req.AckWaitInSecs = 3

	// Set invalid subject
	req.Subject = "foo*.bar"
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}
	// Other kinds of invalid subject
	req.Subject = "foo.bar*"
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}
	req.Subject = "foo.>.*"
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Set valid subject, still no client ID specified
	req.Subject = "foo"
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Set ClientID, should complain that it does not know about clientName
	req.ClientID = clientName
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// TODO: This may change if we fix startSequenceValid
	// Set a start position that we don't have
	req.StartPosition = pb.StartPosition_SequenceStart
	req.StartSequence = 100
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// TODO: This may change if we fix startTimeValid
	// Set a start position that we don't have
	req.StartPosition = pb.StartPosition_TimeDeltaStart
	req.StartTimeDelta = int64(10 * time.Second)
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	req.StartPosition = pb.StartPosition_NewOnly
	// Set DurableName and QGroup
	req.DurableName = "mydur"
	req.QGroup = "mygroup"
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// A durable
	req.DurableName = "mydur"
	req.QGroup = ""
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// A queue subscriber
	req.DurableName = ""
	req.QGroup = "mygroup"
	if err := sendInvalidSubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// There should be no client created
	clients := s.clients.GetClients()
	if len(clients) > 0 {
		t.Fatalf("Incorrect number of clients, expected 0, got %v", len(clients))
	}

	// But channel "foo" should have been created though
	if !s.store.HasChannel() {
		t.Fatal("Expected channel foo to have been created")
	}
}

func sendInvalidUnsubRequest(s *StanServer, nc *nats.Conn, req *pb.UnsubscribeRequest) error {
	b, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("Error during marshal: %v", err)
	}
	rep, err := nc.Request(s.info.Unsubscribe, b, time.Second)
	if err != nil {
		return fmt.Errorf("Unexpected error: %v", err)
	}
	// Check response
	subRep := &pb.SubscriptionResponse{}
	subRep.Unmarshal(rep.Data)

	// Expect error
	if subRep.Error == "" {
		return fmt.Errorf("Expected error, got none")
	}
	return nil
}

func TestInvalidUnsubRequest(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	// Use a bare NATS connection to send incorrect requests
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	sc, err := stan.Connect(clusterName, clientName, stan.NatsConn(nc))
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}

	// Create a valid subscription first
	sub, err := sc.Subscribe("foo", func(_ *stan.Msg) {})
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Verify server state. Client should be created
	client := s.clients.Lookup(clientName)
	if client == nil {
		t.Fatal("A client should have been created")
	}
	subs := s.clients.GetSubs(clientName)
	if subs == nil || len(subs) != 1 {
		t.Fatalf("Expected to have 1 sub, got %v", len(subs))
	}

	// Create empty request
	req := &pb.UnsubscribeRequest{}

	// Send this empty request
	if err := sendInvalidUnsubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Unsubscribe for a subject we did not subscribe to
	req.Subject = "bar"
	if err := sendInvalidUnsubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Invalid ack inbox
	req.Subject = "foo"
	req.ClientID = clientName
	req.Inbox = "wrong"
	if err := sendInvalidUnsubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Correct subject, inbox, but invalid ClientID
	req.Subject = "foo"
	req.Inbox = subs[0].AckInbox
	req.ClientID = "wrong"
	if err := sendInvalidUnsubRequest(s, nc, req); err != nil {
		t.Fatalf("%v", err)
	}

	// Valid unsubscribe
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unexpected error on unsubscribe: %v\n", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Check that sub's has been removed.
	subs = s.clients.GetSubs(clientName)
	if len(subs) != 0 {
		t.Fatalf("Expected to have 0 sub, got %v", len(subs))
	}
}

func TestDuplicateClientIDs(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	c1 := NewDefaultConnection(t)
	defer c1.Close()

	if c2, err := stan.Connect(clusterName, clientName); err == nil || err.Error() != ErrInvalidClient.Error() {
		if c2 != nil {
			c2.Close()
		}
		t.Fatalf("Expected to get error %q, got %q", ErrInvalidClient, err)
	}

	// Check that there only one client registered
	clients := s.clients.GetClients()
	if len(clients) != 1 {
		t.Fatalf("Incorrect number of clients: %v", len(clients))
	}
}

func TestRedelivery(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc, err := stan.Connect(clusterName, clientName)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer sc.Close()

	rch := make(chan bool)
	cb := func(m *stan.Msg) {
		if m.Redelivered {
			m.Ack()
			rch <- true
		}
	}

	// Create a plain sub
	if _, err := sc.Subscribe("foo", cb, stan.SetManualAckMode(),
		stan.AckWait(time.Second)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Send first message
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	// Add a delay before the next message
	time.Sleep(500 * time.Millisecond)
	// Send second message
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	subs := s.clients.GetSubs(clientName)
	if subs == nil || len(subs) != 1 {
		t.Fatalf("Expected one subscription, got %v", len(subs))
	}
	func(sub *subState) {
		sub.RLock()
		defer sub.RUnlock()
		if sub.acksPending == nil || len(sub.acksPending) != 2 {
			t.Fatalf("Expected to have two ackPending, got %v", len(sub.acksPending))
		}
		if sub.ackTimer == nil {
			t.Fatalf("Expected timer to be set")
		}
	}(subs[0])

	for i := 0; i < 2; i++ {
		if err := Wait(rch); err != nil {
			t.Fatalf("Messages not redelivered")
		}
	}

	// Wait for another ackWait to check if timer is cleared
	time.Sleep(1250 * time.Millisecond)

	// Check state
	func(sub *subState) {
		sub.RLock()
		defer sub.RUnlock()
		if len(sub.acksPending) != 0 {
			t.Fatalf("Expected to have no ackPending, got %v", len(sub.acksPending))
		}
		if sub.ackTimer != nil {
			t.Fatalf("Expected timer to be nil")
		}
	}(subs[0])
}

func TestRedeliveryRace(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	sub, err := sc.Subscribe("foo", func(_ *stan.Msg) {}, stan.AckWait(time.Second), stan.SetManualAckMode())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	time.Sleep(time.Second)
	sub.Unsubscribe()
}

func TestQueueRedelivery(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc, err := stan.Connect(clusterName, clientName)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer sc.Close()

	rch := make(chan bool)
	cb := func(m *stan.Msg) {
		if m.Redelivered {
			m.Ack()
			rch <- true
		}
	}

	// Create a queue subscriber
	if _, err := sc.QueueSubscribe("foo", "group", cb, stan.SetManualAckMode(),
		stan.AckWait(time.Second)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Send first message
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	// Add a delay before the next message
	time.Sleep(500 * time.Millisecond)
	// Send second message
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	subs := s.clients.GetSubs(clientName)
	if subs == nil || len(subs) != 1 {
		t.Fatalf("Expected one subscription, got %v", len(subs))
	}
	func(sub *subState) {
		sub.RLock()
		defer sub.RUnlock()
		if sub.acksPending == nil || len(sub.acksPending) != 2 {
			t.Fatalf("Expected to have two ackPending, got %v", len(sub.acksPending))
		}
		if sub.ackTimer == nil {
			t.Fatalf("Expected timer to be set")
		}
	}(subs[0])

	for i := 0; i < 2; i++ {
		if err := Wait(rch); err != nil {
			t.Fatalf("Messages not redelivered")
		}
	}

	// Wait for another ackWait to check if timer is cleared
	time.Sleep(1250 * time.Millisecond)

	// Check state
	func(sub *subState) {
		sub.RLock()
		defer sub.RUnlock()
		if len(sub.acksPending) != 0 {
			t.Fatalf("Expected to have no ackPending, got %v", len(sub.acksPending))
		}
		if sub.ackTimer != nil {
			t.Fatalf("Expected timer to be nil")
		}
	}(subs[0])
}

func TestDurableRedelivery(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	ch := make(chan bool)
	rch := make(chan bool)
	errors := make(chan error, 5)
	count := 0
	cb := func(m *stan.Msg) {
		count++
		switch count {
		case 1:
			ch <- true
		case 2:
			rch <- true
		default:
			errors <- fmt.Errorf("Unexpected message %v", m)
		}
	}

	sc := NewDefaultConnection(t)
	defer sc.Close()

	_, err := sc.Subscribe("foo", cb, stan.DurableName("dur"), stan.SetManualAckMode())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Wait for first message to be received
	if err := Wait(ch); err != nil {
		t.Fatal("Failed to receive first message")
	}

	// Report error if any
	if len(errors) > 0 {
		t.Fatalf("%v", <-errors)
	}

	// Close the client
	sc.Close()

	// Restart client
	sc2 := NewDefaultConnection(t)
	defer sc2.Close()

	sub2, err := sc2.Subscribe("foo", cb, stan.DurableName("dur"), stan.SetManualAckMode())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub2.Unsubscribe()

	// Wait for redelivered message
	if err := Wait(rch); err != nil {
		t.Fatal("Messages were not redelivered to durable")
	}

	// Report error if any
	if len(errors) > 0 {
		t.Fatalf("%v", <-errors)
	}
}

func TestTooManyChannelsOnCreateSub(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	newLimits := stores.DefaultChannelLimits
	newLimits.MaxChannels = 1
	s.store.SetChannelLimits(newLimits)

	sc := NewDefaultConnection(t)
	defer sc.Close()

	// That should create channel foo
	if _, err := sc.Subscribe("foo", func(_ *stan.Msg) {}); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// This should fail because we reached the limit
	if _, err := sc.Subscribe("bar", func(_ *stan.Msg) {}); err == nil {
		t.Fatalf("Expected error due to too many channels, got none")
	}
}

func TestTooManyChannelsOnPublish(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	newLimits := stores.DefaultChannelLimits
	newLimits.MaxChannels = 1
	s.store.SetChannelLimits(newLimits)

	sc := NewDefaultConnection(t)
	defer sc.Close()

	// That should create channel foo
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// This should fail since we reached the max channels limit
	if err := sc.Publish("bar", []byte("hello")); err == nil {
		t.Fatalf("Expected error due to too many channels, got none")
	}

	// Check that channel bar was not created
	if s.store.LookupChannel("bar") != nil {
		t.Fatal("Channel bar should not have been created")
	}
}

func TestTooManySubs(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	newLimits := stores.DefaultChannelLimits
	newLimits.MaxSubs = 1
	s.store.SetChannelLimits(newLimits)

	sc := NewDefaultConnection(t)
	defer sc.Close()

	// This should be ok
	if _, err := sc.Subscribe("foo", func(_ *stan.Msg) {}); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	// We should get an error here
	if _, err := sc.Subscribe("foo", func(_ *stan.Msg) {}); err == nil {
		t.Fatal("Expected error on subscribe, go none")
	}
	cs := s.store.LookupChannel("foo")
	if cs == nil || cs.UserData == nil {
		t.Fatal("Expected channel to exist")
	}
	ss := cs.UserData.(*subStore)
	func() {
		ss.RLock()
		defer ss.RUnlock()
		if ss.psubs == nil || len(ss.psubs) != 1 {
			t.Fatalf("Expected only one subscription, got %v", len(ss.psubs))
		}
	}()
}

func TestRunServerWithFileStore(t *testing.T) {
	cleanupDatastore(t, defaultDataStore)
	defer cleanupDatastore(t, defaultDataStore)

	opts := DefaultOptions
	opts.StoreType = stores.TypeFile
	opts.FilestoreDir = defaultDataStore
	s := RunServerWithOpts(&opts, nil)
	defer s.Shutdown()

	// Create our own NATS connection to control reconnect wait
	nc, err := nats.Connect(nats.DefaultURL, nats.ReconnectWait(500*time.Millisecond))
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}

	sc, err := stan.Connect(opts.ID, clientName, stan.NatsConn(nc))
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer sc.Close()

	rch := make(chan bool)
	cb := func(m *stan.Msg) {
		rch <- true
	}

	// 2 Queue subscribers on bar
	if _, err := sc.QueueSubscribe("bar", "group", cb); err != nil {
		t.Fatalf("Unexpected error on queue subscribe: %v", err)
	}
	if _, err := sc.QueueSubscribe("bar", "group", cb); err != nil {
		t.Fatalf("Unexpected error on queue subscribe: %v", err)
	}
	// 1 Durable on baz
	if _, err := sc.Subscribe("baz", cb, stan.DurableName("mydur")); err != nil {
		t.Fatalf("Unexpected error on durable subscribe: %v", err)
	}
	// 1 Plain subscriber on foo
	if _, err := sc.Subscribe("foo", cb); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Publish some messages.
	if err := sc.Publish("bar", []byte("Msg for bar")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	if err := sc.Publish("baz", []byte("Msg for baz")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	if err := sc.Publish("foo", []byte("Msg for foo")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := Wait(rch); err != nil {
			t.Fatal("Did not receive message")
		}
	}

	// Wait a bit for the acks to be processed
	time.Sleep(50 * time.Millisecond)

	// Shutdown server
	s.Shutdown()

	// Recover
	s = RunServerWithOpts(&opts, nil)
	defer s.Shutdown()

	// Check server recovered state
	// Should be 1 client
	clients := s.clients.GetClients()
	if clients == nil || len(clients) != 1 {
		t.Fatalf("Unexpected recovered clients: %v", len(clients))
	}

	// Should be 4 subscriptions
	subs := s.clients.GetSubs(clientName)
	if subs == nil || len(subs) != 4 {
		t.Fatalf("Unexpected recovered subs: %v", len(subs))
	}

	// Check details now.
	// 2 Queue subscribers on bar
	cs := s.store.LookupChannel("bar")
	if cs == nil || cs.UserData == nil {
		t.Fatal("Expected channel bar to exist")
	}
	func() {
		ss := cs.UserData.(*subStore)
		ss.RLock()
		defer ss.RUnlock()
		if len(ss.durables) != 0 {
			t.Fatalf("Unexpected durables for bar: %v", len(ss.durables))
		}
		if len(ss.psubs) != 0 {
			t.Fatalf("Unexpected plain subscribers for bar: %v", len(ss.psubs))
		}
		if len(ss.qsubs) != 1 {
			t.Fatalf("Expected one queue group for bar, got: %v", len(ss.qsubs))
		}
		qs := ss.qsubs["group"]
		if qs == nil {
			t.Fatal("Expected to get a queue state")
		}
		qs.RLock()
		qsubs := qs.subs
		qs.RUnlock()
		if qsubs == nil || len(qsubs) != 2 {
			t.Fatalf("Unexpected number of queue subscribers of group 'group' for channel bar, got: %v", len(qsubs))
		}
	}()

	// One durable on baz
	cs = s.store.LookupChannel("baz")
	if cs == nil || cs.UserData == nil {
		t.Fatal("Expected channel baz to exist")
	}
	func() {
		ss := cs.UserData.(*subStore)
		ss.RLock()
		defer ss.RUnlock()
		if len(ss.durables) != 1 {
			t.Fatalf("Expected one durable for baz: %v", len(ss.durables))
		}
		// Durables are both in plain subs and durables
		if len(ss.psubs) != 1 {
			t.Fatalf("Unexpected plain subscribers for baz: %v", len(ss.psubs))
		}
		if len(ss.qsubs) != 0 {
			t.Fatalf("Unexpected queue groups for baz, got: %v", len(ss.qsubs))
		}
	}()

	// One plain subscriber on foo
	cs = s.store.LookupChannel("foo")
	if cs == nil || cs.UserData == nil {
		t.Fatal("Expected channel foo to exist")
	}
	func() {
		ss := cs.UserData.(*subStore)
		ss.RLock()
		defer ss.RUnlock()
		if len(ss.durables) != 0 {
			t.Fatalf("Unexpected durables for foo: %v", len(ss.durables))
		}
		if len(ss.psubs) != 1 {
			t.Fatalf("Expected 1 plain subscriber for foo: %v", len(ss.psubs))
		}
		if len(ss.qsubs) != 0 {
			t.Fatalf("Unexpected queue subscribers for foo, got: %v", len(ss.qsubs))
		}
	}()

	// Wait more than the reconnect wait, to make sure that
	// the new publisher's new message is delivered
	time.Sleep(700 * time.Millisecond)

	// Send new messages, should be received.
	if err := sc.Publish("bar", []byte("New Msg for bar")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	if err := sc.Publish("baz", []byte("New Msg for baz")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	if err := sc.Publish("foo", []byte("New Msg for foo")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Wait for the old and new messages
	for i := 0; i < 6; i++ {
		if err := Wait(rch); err != nil {
			t.Fatal("Did not receive the old message")
		}
	}
}

func checkDurable(t *testing.T, s *StanServer, channel, durName, durKey string) {
	c := s.clients.Lookup(clientName)
	if c == nil {
		stackFatalf(t, "Expected client %v to be registered", clientName)
	}
	c.RLock()
	subs := c.subs
	c.RUnlock()
	if len(subs) != 1 {
		stackFatalf(t, "Expected 1 sub, got %v", len(subs))
	}
	sub := subs[0]
	if sub.DurableName != durName {
		stackFatalf(t, "Expected durable name %v, got %v", durName, sub.DurableName)
	}
	// Check that durable is also in subStore
	cs := s.store.LookupChannel(channel)
	if cs == nil {
		stackFatalf(t, "Expected channel %q to be created", channel)
	}
	ss := cs.UserData.(*subStore)
	ss.RLock()
	durInSS := ss.durables[durKey]
	ss.RUnlock()
	if durInSS == nil || durInSS.DurableName != durName {
		stackFatalf(t, "Expected durable to be in subStore")
	}
}

func TestDurableCanReconnect(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	cb := func(_ *stan.Msg) {}

	durName := "mydur"
	sr := &pb.SubscriptionRequest{
		ClientID:    clientName,
		Subject:     "foo",
		DurableName: durName,
	}
	durKey := durableKey(sr)

	// Create durable
	if _, err := sc.Subscribe("foo", cb, stan.DurableName(durName)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Check durable is created
	checkDurable(t, s, "foo", durName, durKey)

	// Close stan connection
	sc.Close()

	// Connect again
	sc = NewDefaultConnection(t)
	defer sc.Close()

	// Start the durable
	if _, err := sc.Subscribe("foo", cb, stan.DurableName(durName)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Check durable is found
	checkDurable(t, s, "foo", durName, durKey)

	// Close stan connection
	sc.Close()

	// Connect again
	sc = NewDefaultConnection(t)
	defer sc.Close()

	// Start the durable
	if _, err := sc.Subscribe("foo", cb, stan.DurableName(durName)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Check durable is found
	checkDurable(t, s, "foo", durName, durKey)
}

func TestDurableAckedMsgNotRedelivered(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	// Make a channel big enough so that we don't block
	msgs := make(chan *stan.Msg, 10)

	cb := func(m *stan.Msg) {
		msgs <- m
	}

	durName := "mydur"
	sr := &pb.SubscriptionRequest{
		ClientID:    clientName,
		Subject:     "foo",
		DurableName: durName,
	}
	durKey := durableKey(sr)

	// Create durable
	if _, err := sc.Subscribe("foo", cb, stan.DurableName(durName)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Check durable is created
	checkDurable(t, s, "foo", durName, durKey)

	// We verified that there is 1 sub, and this is our durable.
	subs := s.clients.GetSubs(clientName)
	durable := subs[0]
	durable.RLock()
	// Get the AckInbox.
	ackInbox := durable.AckInbox
	// Get the ack subscriber
	ackSub := durable.ackSub
	durable.RUnlock()

	// Send a message
	if err := sc.Publish("foo", []byte("msg1")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Verify message is acked.
	checkDurableNoPendingAck(t, s, true, ackInbox, ackSub, 1)

	// Close stan connection
	sc.Close()

	// Connect again
	sc = NewDefaultConnection(t)
	defer sc.Close()

	// Start the durable
	if _, err := sc.Subscribe("foo", cb, stan.DurableName(durName)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Check durable is found
	checkDurable(t, s, "foo", durName, durKey)

	// Send a second message
	if err := sc.Publish("foo", []byte("msg2")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Verify that we have different AckInbox and ackSub and message is acked.
	checkDurableNoPendingAck(t, s, false, ackInbox, ackSub, 2)

	// Close stan connection
	sc.Close()

	// Connect again
	sc = NewDefaultConnection(t)
	defer sc.Close()

	// Start the durable
	if _, err := sc.Subscribe("foo", cb, stan.DurableName(durName)); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// Check durable is found
	checkDurable(t, s, "foo", durName, durKey)

	// Verify that we have different AckInbox and ackSub and message is acked.
	checkDurableNoPendingAck(t, s, false, ackInbox, ackSub, 2)

	numMsgs := len(msgs)
	if numMsgs > 2 {
		t.Fatalf("Expected only 2 messages to be delivered, got %v", numMsgs)
	}
	for i := 0; i < numMsgs; i++ {
		m := <-msgs
		if m.Redelivered {
			t.Fatal("Unexpected redelivered message")
		}
		if m.Sequence != uint64(i+1) {
			t.Fatalf("Expected message %v's sequence to be %v, got %v", (i + 1), (i + 1), m.Sequence)
		}
	}
}

func checkDurableNoPendingAck(t *testing.T, s *StanServer, isSame bool,
	ackInbox string, ackSub *nats.Subscription, expectedSeq uint64) {
	// When called, we know that there is 1 sub, and the sub is a durable.
	subs := s.clients.GetSubs(clientName)
	durable := subs[0]
	durable.RLock()
	durAckInbox := durable.AckInbox
	durAckSub := durable.ackSub
	durable.RUnlock()

	if isSame {
		if durAckInbox != ackInbox {
			stackFatalf(t, "Expected ackInbox %v, got %v", ackInbox, durAckInbox)
		}
		if durAckSub != ackSub {
			stackFatalf(t, "Expected subscriber on ack to be %p, got %p", ackSub, durAckSub)
		}
	} else {
		if durAckInbox == ackInbox {
			stackFatalf(t, "Expected different ackInbox'es")
		}
		if durAckSub == ackSub {
			stackFatalf(t, "Expected different ackSub")
		}
	}

	limit := time.Now().Add(5 * time.Second)
	for time.Now().Before(limit) {
		durable.RLock()
		lastSent := durable.LastSent
		acks := len(durable.acksPending)
		durable.RUnlock()

		if lastSent != expectedSeq || acks > 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// We are ok
		return
	}
	stackFatalf(t, "Message was not acknowledged")
}

func TestClientCrashAndReconnect(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	sc, err := stan.Connect(clusterName, clientName, stan.NatsConn(nc))
	if err != nil {
		t.Fatalf("Expected to connect correctly, got err %v", err)
	}
	defer sc.Close()

	// should get a duplicate clientID error
	if sc2, err := stan.Connect(clusterName, clientName); err == nil {
		sc2.Close()
		t.Fatal("Expected to be unable to connect")
	}

	// kill the NATS conn
	nc.Close()

	// Since the original client won't respond to a ping, we should
	// be able to connect, and it should not take too long.
	start := time.Now()

	// should succeed
	if sc2, err := stan.Connect(clusterName, clientName); err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	} else {
		defer sc2.Close()
	}

	duration := time.Now().Sub(start)
	if duration > 5*time.Second {
		t.Fatalf("Took too long to be able to connect: %v", duration)
	}
}

func TestStartPositionNewOnly(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	rch := make(chan bool)

	cb := func(_ *stan.Msg) {
		rch <- true
	}

	// Start a subscriber with "NewOnly" as start position.
	// Since there was no message previously sent, it should
	// not receive anything yet.
	sub, err := sc.Subscribe("foo", cb, stan.StartAt(pb.StartPosition_NewOnly))
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Wait a little bit and ensure no message was received
	if err := WaitTime(rch, 500*time.Millisecond); err == nil {
		t.Fatal("No message should have been received")
	}

	// Send a message now.
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Message should be received
	if err := Wait(rch); err != nil {
		t.Fatal("Did not receive our message")
	}

	// Start another subscriber with "NewOnly" as start position.
	sub2, err := sc.Subscribe("foo", cb, stan.StartAt(pb.StartPosition_NewOnly))
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub2.Unsubscribe()

	// It should not receive anything
	if err := WaitTime(rch, 500*time.Millisecond); err == nil {
		t.Fatal("No message should have been received")
	}
}

func TestStartPositionLastReceived(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	rch := make(chan bool)

	cb := func(_ *stan.Msg) {
		rch <- true
	}

	// Start a subscriber with "LastReceived" as start position.
	// Since there was no message previously sent, it should
	// not receive anything yet.
	sub, err := sc.Subscribe("foo", cb, stan.StartWithLastReceived())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Wait a little bit and ensure no message was received
	if err := WaitTime(rch, 500*time.Millisecond); err == nil {
		t.Fatal("No message should have been received")
	}

	// Send a message now.
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Message should be received
	if err := Wait(rch); err != nil {
		t.Fatal("Did not receive our message")
	}

	rch = make(chan bool)

	cb = func(m *stan.Msg) {
		if string(m.Data) == "msg2" {
			rch <- true
		}
	}

	// Send two messages
	if err := sc.Publish("bar", []byte("msg1")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	if err := sc.Publish("bar", []byte("msg2")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Start a subscriber with "LastReceived" as start position.
	sub2, err := sc.Subscribe("bar", cb, stan.StartWithLastReceived())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub2.Unsubscribe()

	// The second message should be received
	if err := Wait(rch); err != nil {
		t.Fatal("Did not receive our message")
	}
}

func TestStartPositionFirstSequence(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	rch := make(chan bool)

	cb := func(_ *stan.Msg) {
		rch <- true
	}

	// Start a subscriber with "FirstSequence" as start position.
	// Since there was no message previously sent, it should
	// not receive anything yet.
	sub, err := sc.Subscribe("foo", cb, stan.DeliverAllAvailable())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Wait a little bit and ensure no message was received
	if err := WaitTime(rch, 500*time.Millisecond); err == nil {
		t.Fatal("No message should have been received")
	}

	// Send a message now.
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Message should be received
	if err := Wait(rch); err != nil {
		t.Fatal("Did not receive our message")
	}

	mch := make(chan *stan.Msg, 2)

	cb = func(m *stan.Msg) {
		mch <- m
	}

	// Send two messages
	if err := sc.Publish("bar", []byte("msg1")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}
	if err := sc.Publish("bar", []byte("msg2")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Start a subscriber with "FirstPosition" as start position.
	sub2, err := sc.Subscribe("bar", cb, stan.DeliverAllAvailable())
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub2.Unsubscribe()

	first := true
	select {
	case m := <-mch:
		if first {
			if string(m.Data) != "msg1" {
				t.Fatalf("Expected msg1 first, got %v", string(m.Data))
			}
			first = false
		} else {
			if string(m.Data) != "msg2" {
				t.Fatalf("Expected msg2 second, got %v", string(m.Data))
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Did not get our message")
	}
}

func TestStartPositionSequenceStart(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	rch := make(chan bool)

	cb := func(_ *stan.Msg) {
		rch <- true
	}

	// Start a subscriber with "Sequence" as start position.
	// As of now, since there is no message, the call will fail.
	sub, err := sc.Subscribe("foo", cb, stan.StartAtSequence(0))
	if err == nil {
		sub.Unsubscribe()
		t.Fatal("Expected error on subscribe, got none")
	}

	// Send a message now.
	if err := sc.Publish("foo", []byte("hello")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Create a new subscriber with "Sequence" 1
	sub2, err := sc.Subscribe("foo", cb, stan.StartAtSequence(1))
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub2.Unsubscribe()

	// Message should be received
	if err := Wait(rch); err != nil {
		t.Fatal("Did not receive our message")
	}
}

func TestStartPositionTimeDelta(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	rch := make(chan bool)

	cb := func(m *stan.Msg) {
		if string(m.Data) == "msg2" {
			rch <- true
		}
	}

	//FIXME(ik): As of now, start at a time delta when no message
	// has been stored would return an error. So test only with
	// messages present.
	// Send a message.
	if err := sc.Publish("foo", []byte("msg1")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Wait 1.5 seconds.
	time.Sleep(1500 * time.Millisecond)

	// Sends a second message
	if err := sc.Publish("foo", []byte("msg2")); err != nil {
		t.Fatalf("Unexpected error on publish: %v", err)
	}

	// Start a subscriber with "TimeDelta" as start position.
	sub, err := sc.Subscribe("foo", cb, stan.StartAtTimeDelta(1*time.Second))
	if err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Message 2 should be received
	if err := Wait(rch); err != nil {
		t.Fatal("Did not receive our message")
	}
}

func TestIgnoreRecoveredSubForUnknownClientID(t *testing.T) {
	cleanupDatastore(t, defaultDataStore)
	defer cleanupDatastore(t, defaultDataStore)

	opts := DefaultOptions
	opts.StoreType = stores.TypeFile
	opts.FilestoreDir = defaultDataStore
	s := RunServerWithOpts(&opts, nil)
	defer s.Shutdown()

	sc := NewDefaultConnection(t)
	defer sc.Close()

	if _, err := sc.Subscribe("foo", func(_ *stan.Msg) {}); err != nil {
		t.Fatalf("Unexpected error on subscribe: %v", err)
	}

	// For delete the client
	s.clients.Unregister(clientName)

	// Shutdown the server
	s.Shutdown()

	// Restart the server
	s = RunServerWithOpts(&opts, nil)
	defer s.Shutdown()

	// Check that client does not exist
	if s.clients.Lookup(clientName) != nil {
		t.Fatal("Client should not have been recovered")
	}
	// Channel would be recovered
	cs := s.store.LookupChannel("foo")
	if cs == nil {
		t.Fatal("Channel foo should have been recovered")
	}
	// But there should not be any subscription
	ss := cs.UserData.(*subStore)
	ss.RLock()
	numSubs := len(ss.psubs)
	ss.RUnlock()
	if numSubs > 0 {
		t.Fatalf("Should not have restored subscriptions, got %v", numSubs)
	}
}

func TestCheckClientHealth(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	// Override HB settings
	s.Lock()
	s.hbInterval = 200 * time.Millisecond
	s.hbTimeout = 10 * time.Millisecond
	s.maxFailedHB = 10
	s.Unlock()

	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	sc, err := stan.Connect(clusterName, clientName, stan.NatsConn(nc))
	if err != nil {
		t.Fatalf("Expected to connect correctly, got err %v", err)
	}
	defer sc.Close()

	// kill the NATS conn
	nc.Close()

	// Check that the server closes the connection
	ok := false
	timeout := time.Now().Add(5 * time.Second)
	for time.Now().Before(timeout) {
		clients := s.clients.GetClients()
		if len(clients) > 0 {
			time.Sleep(100 * time.Millisecond)
		} else {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatal("Timeout before client purged")
	}
}

func TestConnectsWithDupCID(t *testing.T) {
	s := RunServer(clusterName)
	defer s.Shutdown()

	// Not too small to avoid flapping tests.
	s.dupCIDTimeout = 1 * time.Second
	s.dupMaxCIDRoutines = 5
	total := int(s.dupMaxCIDRoutines)

	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Fatalf("Unexpected error on connect: %v", err)
	}
	defer nc.Close()

	dupCIDName := "dupCID"

	sc, err := stan.Connect(clusterName, dupCIDName, stan.NatsConn(nc))
	if err != nil {
		t.Fatalf("Expected to connect correctly, got err %v", err)
	}
	defer sc.Close()

	// Close the nc connection
	nc.Close()

	var wg sync.WaitGroup

	// Channel large enough to hold all possible errors.
	errors := make(chan error, 3*total)

	dupTimeoutMin := time.Duration(float64(s.dupCIDTimeout) * 0.9)
	dupTimeoutMax := time.Duration(float64(s.dupCIDTimeout) * 1.1)

	wg.Add(1)

	connect := func(cid string, shouldFail bool) (stan.Conn, time.Duration, error) {
		start := time.Now()
		c, err := stan.Connect(clusterName, cid, stan.ConnectWait(3*s.dupCIDTimeout))
		duration := time.Now().Sub(start)
		if shouldFail {
			if c != nil {
				c.Close()
			}
			if err == nil || err == stan.ErrConnectReqTimeout {
				return nil, 0, fmt.Errorf("Connect should have failed")
			}
			return nil, duration, nil
		} else if err != nil {
			return nil, 0, err
		}
		return c, duration, nil
	}

	getErrors := func() string {
		errorsStr := ""
		numErrors := len(errors)
		for i := 0; i < numErrors; i++ {
			e := <-errors
			oneErr := fmt.Sprintf("%d: %s\n", (i + 1), e.Error())
			if i == 0 {
				errorsStr = "\n"
			}
			errorsStr = errorsStr + oneErr
		}
		return errorsStr
	}

	// Start this go routine that will try to connect 2*total-1
	// times. These all should fail (quickly) since the one
	// connecting below should be the one that connects.
	go func() {
		defer wg.Done()
		time.Sleep(s.dupCIDTimeout / 2)
		for i := 0; i < 2*total-1; i++ {
			_, duration, err := connect(dupCIDName, true)
			if err != nil {
				errors <- err
				continue
			}
			// These should fail "immediately", so consider it a failure if
			// it is close to the dupCIDTimeout
			if duration >= dupTimeoutMin {
				errors <- fmt.Errorf("Connect took too long to fail: %v", duration)
			}
		}
	}()

	// This connection on different client ID should not take long
	newConn, duration, err := connect("newCID", false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer newConn.Close()
	if duration >= dupTimeoutMin {
		t.Fatalf("Connect expected to be fast, took %v", duration)
	}

	// This one should connect, and it should take close to dupCIDTimeout
	replaceConn, duration, err := connect(dupCIDName, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer replaceConn.Close()
	if duration < dupTimeoutMin || duration > dupTimeoutMax {
		t.Fatalf("Connect expected in the range [%v-%v], took %v",
			dupTimeoutMin, dupTimeoutMax, duration)
	}

	// Wait for all other connects to complete
	wg.Wait()

	// Report possible errors
	if errs := getErrors(); errs != "" {
		t.Fatalf("Test failed: %v", errs)
	}

	// We don't need those anymore.
	newConn.Close()
	replaceConn.Close()

	// Now, let's create (total + 1) connections with different CIDs
	// and close their NATS connection. Then try to "reconnect".
	// The first (total) connections should each take about dupCIDTimeout to
	// complete.
	// The last (total + 1) connection should be delayed waiting for
	// a go routine to finish. So the expected duration - assuming that
	// they all start roughly at the same time - would be 2 * dupCIDTimeout.
	for i := 0; i < total+1; i++ {
		nc, err := nats.Connect(nats.DefaultURL)
		if err != nil {
			t.Fatalf("Unexpected error on connect: %v", err)
		}
		defer nc.Close()

		cid := fmt.Sprintf("%s_%d", dupCIDName, i)
		sc, err := stan.Connect(clusterName, cid, stan.NatsConn(nc))
		if err != nil {
			t.Fatalf("Expected to connect correctly, got err %v", err)
		}
		defer sc.Close()

		// Close the nc connection
		nc.Close()
	}

	wg.Add(total + 1)

	// Need to close the connections only after the test is done
	conns := make([]stan.Conn, total+1)

	// Cleanup function
	cleanupConns := func() {
		wg.Wait()
		for _, c := range conns {
			c.Close()
		}
	}

	var delayedGuard sync.Mutex
	delayed := false

	// Connect 1 more than the max number of allowed go routines.
	for i := 0; i < total+1; i++ {
		go func(idx int) {
			defer wg.Done()
			cid := fmt.Sprintf("%s_%d", dupCIDName, idx)
			c, duration, err := connect(cid, false)
			if err != nil {
				errors <- err
				return
			}
			conns[idx] = c
			ok := false
			if duration >= dupTimeoutMin && duration <= dupTimeoutMax {
				ok = true
			}
			if !ok && duration >= 2*dupTimeoutMin && duration <= 2*dupTimeoutMax {
				delayedGuard.Lock()
				if delayed {
					delayedGuard.Unlock()
					errors <- fmt.Errorf("Failing %q, only one connection should take that long", cid)
					return
				}
				delayed = true
				delayedGuard.Unlock()
				return
			}
			if !ok {
				if duration < dupTimeoutMin || duration > dupTimeoutMax {
					errors <- fmt.Errorf("Connect with cid %q expected in the range [%v-%v], took %v",
						cid, dupTimeoutMin, dupTimeoutMax, duration)
				}
			}
		}(i)
	}

	// Wait for all routines to return
	wg.Wait()

	// Wait for other connects to complete, and close them.
	cleanupConns()

	// Report possible errors
	if errs := getErrors(); errs != "" {
		t.Fatalf("Test failed: %v", errs)
	}
}

func TestStoreTypeUnknown(t *testing.T) {
	cleanupDatastore(t, defaultDataStore)
	defer cleanupDatastore(t, defaultDataStore)

	opts := DefaultOptions
	opts.StoreType = "MyType"

	var failedServer *StanServer
	defer func() {
		if r := recover(); r == nil {
			if failedServer != nil {
				failedServer.Shutdown()
			}
			t.Fatal("Server should have failed with a panic because of unknown store type")
		}
	}()
	failedServer = RunServerWithOpts(&opts, nil)
}

func TestFileStoreMissingDirectory(t *testing.T) {
	cleanupDatastore(t, defaultDataStore)
	defer cleanupDatastore(t, defaultDataStore)

	opts := DefaultOptions
	opts.StoreType = stores.TypeFile
	opts.FilestoreDir = ""

	var failedServer *StanServer
	defer func() {
		if r := recover(); r == nil {
			if failedServer != nil {
				failedServer.Shutdown()
			}
			t.Fatal("Server should have failed with a panic because missing directory")
		}
	}()
	failedServer = RunServerWithOpts(&opts, nil)
}

func TestFileStoreChangedClusterID(t *testing.T) {
	cleanupDatastore(t, defaultDataStore)
	defer cleanupDatastore(t, defaultDataStore)

	opts := DefaultOptions
	opts.StoreType = stores.TypeFile
	opts.FilestoreDir = defaultDataStore
	s := RunServerWithOpts(&opts, nil)
	s.Shutdown()

	var failedServer *StanServer
	defer func() {
		if r := recover(); r == nil {
			if failedServer != nil {
				failedServer.Shutdown()
			}
			t.Fatal("Server should have failed with a panic because of different IDs")
		}
	}()
	// Change cluster ID, running the server should fail with a panic
	opts.ID = "differentID"
	failedServer = RunServerWithOpts(&opts, nil)
}
