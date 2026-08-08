// Package a2atape implements the A2A 1.0 taskstore.Store contract on top of
// Tape's append-only storage.
//
// Every operation authenticates its caller and maps the returned principal to
// a Tape owner. Applications must provide an Authenticator that returns a
// stable, non-empty principal for every A2A server call. Task and context IDs
// supplied by a peer never select the local owner.
//
// Install a Store in the official server and add the persistence interceptor
// when taskless Message responses must also be recorded:
//
//	store, err := a2atape.NewStore(a2atape.Config{
//		Storage:       tapeStorage,
//		Authenticator: authenticate,
//	})
//	if err != nil {
//		return err
//	}
//	handler := a2asrv.NewHandler(
//		executor,
//		a2asrv.WithTaskStore(store),
//		a2asrv.WithCallInterceptors(a2atape.NewPersistenceInterceptor(store)),
//	)
//
// Tape sequence numbers back taskstore.TaskVersion values but remain private
// implementation details; they are never protocol identifiers. Recovery uses
// a per-owner projection that is rebuilt from Tape on first access and then
// advanced by incrementally replaying new records. Corrupt, inconsistent, or
// unknown-version records fail closed with their Tape sequence and record ID.
//
// Concurrency control is process-local per owner. Cross-process strong OCC
// requires a future conditional-append/CAS capability in storage.TapeStorage.
//
// The implementation is tested against github.com/a2aproject/a2a-go/v2
// v2.3.1. Its living specification uses github.com/cucumber/godog v0.15.1,
// and structural validation uses github.com/go-playground/validator/v10
// v10.30.2.
package a2atape

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

var _ taskstore.Store = (*Store)(nil)
var _ a2asrv.CallInterceptor = (*PersistenceInterceptor)(nil)
