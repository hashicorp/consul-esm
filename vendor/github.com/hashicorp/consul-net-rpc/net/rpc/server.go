// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Package rpc provides access to the exported methods of an object across a
network or other I/O connection.  A server registers an object, making it visible
as a service with the name of the type of the object.  After registration, exported
methods of the object will be accessible remotely.  A server may register multiple
objects (services) of different types but it is an error to register multiple
objects of the same type.

Only methods that satisfy these criteria will be made available for remote access;
other methods will be ignored:

  - the method's type is exported.
  - the method is exported.
  - the method has two arguments, both exported (or builtin) types.
  - the method's second argument is a pointer.
  - the method has return type error.

In effect, the method must look schematically like

	func (t *T) MethodName(argType T1, replyType *T2) error

where T1 and T2 can be marshaled by encoding/gob.
These requirements apply even if a different codec is used.
(In the future, these requirements may soften for custom codecs.)

The method's first argument represents the arguments provided by the caller; the
second argument represents the result parameters to be returned to the caller.
The method's return value, if non-nil, is passed back as a string that the client
sees as if created by errors.New.  If an error is returned, the reply parameter
will not be sent back to the client.

The server may handle requests on a single connection by calling ServeConn.  More
typically it will create a network listener and call Accept or, for an HTTP
listener, HandleHTTP and http.Serve.

A client wishing to use the service establishes a connection and then invokes
NewClient on the connection.  The convenience function Dial (DialHTTP) performs
both steps for a raw network connection (an HTTP connection).  The resulting
Client object has two methods, Call and Go, that specify the service and method to
call, a pointer containing the arguments, and a pointer to receive the result
parameters.

The Call method waits for the remote call to complete while the Go method
launches the call asynchronously and signals completion using the Call
structure's Done channel.

Unless an explicit codec is set up, package encoding/gob is used to
transport the data.

Here is a simple example.  A server wishes to export an object of type Arith:

	package server

	import "errors"

	type Args struct {
		A, B int
	}

	type Quotient struct {
		Quo, Rem int
	}

	type Arith int

	func (t *Arith) Multiply(args *Args, reply *int) error {
		*reply = args.A * args.B
		return nil
	}

	func (t *Arith) Divide(args *Args, quo *Quotient) error {
		if args.B == 0 {
			return errors.New("divide by zero")
		}
		quo.Quo = args.A / args.B
		quo.Rem = args.A % args.B
		return nil
	}

The server calls (for HTTP service):

	arith := new(Arith)
	rpc.Register(arith)
	rpc.HandleHTTP()
	l, e := net.Listen("tcp", ":1234")
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)

At this point, clients can see a service "Arith" with methods "Arith.Multiply" and
"Arith.Divide".  To invoke one, a client first dials the server:

	client, err := rpc.DialHTTP("tcp", serverAddress + ":1234")
	if err != nil {
		log.Fatal("dialing:", err)
	}

Then it can make a remote call:

	// Synchronous call
	args := &server.Args{7,8}
	var reply int
	err = client.Call("Arith.Multiply", args, &reply)
	if err != nil {
		log.Fatal("arith error:", err)
	}
	fmt.Printf("Arith: %d*%d=%d", args.A, args.B, reply)

or

	// Asynchronous call
	quotient := new(Quotient)
	divCall := client.Go("Arith.Divide", args, quotient, nil)
	replyCall := <-divCall.Done	// will be equal to divCall
	// check errors, print, etc.

A server implementation will often provide a simple, type-safe wrapper for the
client.

The net/rpc package is frozen and is not accepting new features.
*/
package rpc

import (
	"bufio"
	"context"
	"encoding/gob"
	"errors"
	"go/token"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
)

const (
	// Defaults used by HandleHTTP
	DefaultRPCPath   = "/_goRPC_"
	DefaultDebugPath = "/debug/rpc"
)

// Precompute the reflect type for error. Can't use error directly
// because Typeof takes an empty interface value. This is annoying.
var typeOfError = reflect.TypeOf((*error)(nil)).Elem()

var typeOfContext = reflect.TypeOf((*context.Context)(nil)).Elem()

type methodType struct {
	sync.Mutex // protects counters
	method     reflect.Method
	ArgType    reflect.Type
	ReplyType  reflect.Type
	HasContext bool
	numCalls   uint
}

type service struct {
	name   string                 // name of service
	rcvr   reflect.Value          // receiver of methods for the service
	typ    reflect.Type           // type of the receiver
	method map[string]*methodType // registered methods
}

// Request is a header written before every RPC call. It is used internally
// but documented here as an aid to debugging, such as when analyzing
// network traffic.
type Request struct {
	ServiceMethod string   // format: "Service.Method"
	Seq           uint64   // sequence number chosen by client
	next          *Request // for free list in Server
}

// Response is a header written before every RPC return. It is used internally
// but documented here as an aid to debugging, such as when analyzing
// network traffic.
type Response struct {
	ServiceMethod string    // echoes that of the Request
	Seq           uint64    // echoes that of the request
	Error         string    // error, if any.
	next          *Response // for free list in Server
}

// Server represents an RPC Server.
type Server struct {
	serviceMap sync.Map   // map[string]*service
	reqLock    sync.Mutex // protects freeReq
	freeReq    *Request
	respLock   sync.Mutex // protects freeResp
	freeResp   *Response

	serverServiceCallInterceptor ServerServiceCallInterceptor
	preBodyInterceptor           PreBodyInterceptor
}

// NewServer returns a new Server.
func NewServer() *Server {
	return NewServerWithOpts()
}

// NewServerWithOpts returns a new Server with the following functional options.
func NewServerWithOpts(options ...func(*Server)) *Server {
	s := &Server{}
	for _, option := range options {
		option(s)
	}
	return s
}

func WithServerServiceCallInterceptor(interceptor ServerServiceCallInterceptor) func(*Server) {
	return func(s *Server) {
		s.serverServiceCallInterceptor = interceptor
	}
}

func WithPreBodyInterceptor(interceptor PreBodyInterceptor) func(*Server) {
	return func(s *Server) {
		s.preBodyInterceptor = interceptor
	}
}

// ServerServiceCallInterceptor acts a middleware hook on the server side of the RPC call. The interceptor must
// invoke the handler argument for the RPC request to continue.
type ServerServiceCallInterceptor func(reqServiceMethod string, argv, replyv reflect.Value, handler func() error)

// PreBodyInterceptor acts a middleware hook on the server side of the RPC call that executes as early as possible
// in the flow of execution. Specifically, after the request header is parsed but before the request body is parsed.
// Returning an error will cease further processing of the request and return a response containing the error.
type PreBodyInterceptor func(reqServiceMethod string, sourceAddr net.Addr) error

// DefaultServer is the default instance of *Server.
var DefaultServer = NewServer()

// Is this type exported or a builtin?
func isExportedOrBuiltinType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// PkgPath will be non-empty even for an exported type,
	// so we need to check the type name as well.
	return token.IsExported(t.Name()) || t.PkgPath() == ""
}

// Register publishes in the server the set of methods of the
// receiver value that satisfy the following conditions:
//   - optional context.Context
//   - exported method of exported type
//   - two arguments, both of exported type
//   - the second argument is a pointer
//   - one return value, of type error
//
// It returns an error if the receiver is not an exported type or has
// no suitable methods. It also logs the error using package log.
// The client accesses each method using a string of the form "Type.Method",
// where Type is the receiver's concrete type.
func (server *Server) Register(rcvr interface{}) error {
	return server.register(rcvr, "", false)
}

// RegisterName is like Register but uses the provided name for the type
// instead of the receiver's concrete type.
func (server *Server) RegisterName(name string, rcvr interface{}) error {
	return server.register(rcvr, name, true)
}

func (server *Server) register(rcvr interface{}, name string, useName bool) error {
	s := new(service)
	s.typ = reflect.TypeOf(rcvr)
	s.rcvr = reflect.ValueOf(rcvr)
	sname := reflect.Indirect(s.rcvr).Type().Name()
	if useName {
		sname = name
	}
	if sname == "" {
		s := "rpc.Register: no service name for type " + s.typ.String()
		log.Print(s)
		return errors.New(s)
	}
	if !token.IsExported(sname) && !useName {
		s := "rpc.Register: type " + sname + " is not exported"
		log.Print(s)
		return errors.New(s)
	}
	s.name = sname

	// Install the methods
	s.method = suitableMethods(s.typ, true)

	if len(s.method) == 0 {
		str := ""

		// To help the user, see if a pointer receiver would work.
		method := suitableMethods(reflect.PtrTo(s.typ), false)
		if len(method) != 0 {
			str = "rpc.Register: type " + sname + " has no exported methods of suitable type (hint: pass a pointer to value of that type)"
		} else {
			str = "rpc.Register: type " + sname + " has no exported methods of suitable type"
		}
		log.Print(str)
		return errors.New(str)
	}

	if _, dup := server.serviceMap.LoadOrStore(sname, s); dup {
		return errors.New("rpc: service already defined: " + sname)
	}
	return nil
}

// suitableMethods returns suitable Rpc methods of typ, it will report
// error using log if reportErr is true.
func suitableMethods(typ reflect.Type, reportErr bool) map[string]*methodType {
	methods := make(map[string]*methodType)
	for m := 0; m < typ.NumMethod(); m++ {
		method := typ.Method(m)
		mtype := method.Type
		mname := method.Name
		// Method must be exported.
		if !method.IsExported() {
			continue
		}

		// Determine if we have a leading context or not.
		var (
			ctxType   reflect.Type
			argOffset int
		)
		if mtype.NumIn() == 4 {
			// First type if there are 4 args must be a ctx.
			ctxType = mtype.In(1)
			if ctxType != typeOfContext {
				if reportErr {
					log.Printf("rpc.Register: argument type of method %q must be context.Context when 4 arguments are provided: %q\n", mname, ctxType)
					continue
				}
			}
			argOffset = 1
		}

		// Method needs 3-4 ins: ctx (optional), receiver, *args, *reply.
		if mtype.NumIn() != 3+argOffset {
			if reportErr {
				log.Printf("rpc.Register: method %q has %d input parameters; needs exactly %d\n", mname, mtype.NumIn(), 3+argOffset)
			}
			continue
		}

		// First arg need not be a pointer.
		argType := mtype.In(1 + argOffset)
		if !isExportedOrBuiltinType(argType) {
			if reportErr {
				log.Printf("rpc.Register: argument type of method %q is not exported: %q\n", mname, argType)
			}
			continue
		}
		// Second arg must be a pointer.
		replyType := mtype.In(2 + argOffset)
		if replyType.Kind() != reflect.Ptr {
			if reportErr {
				log.Printf("rpc.Register: reply type of method %q is not a pointer: %q\n", mname, replyType)
			}
			continue
		}
		// Reply type must be exported.
		if !isExportedOrBuiltinType(replyType) {
			if reportErr {
				log.Printf("rpc.Register: reply type of method %q is not exported: %q\n", mname, replyType)
			}
			continue
		}
		// Method needs one out.
		if mtype.NumOut() != 1 {
			if reportErr {
				log.Printf("rpc.Register: method %q has %d output parameters; needs exactly one\n", mname, mtype.NumOut())
			}
			continue
		}
		// The return type of the method must be error.
		if returnType := mtype.Out(0); returnType != typeOfError {
			if reportErr {
				log.Printf("rpc.Register: return type of method %q is %q, must be error\n", mname, returnType)
			}
			continue
		}
		methods[mname] = &methodType{
			method:     method,
			ArgType:    argType,
			ReplyType:  replyType,
			HasContext: (ctxType != nil),
		}
	}
	return methods
}

// A value sent as a placeholder for the server's response value when the server
// receives an invalid request. It is never decoded by the client since the Response
// contains an error when it is used.
var invalidRequest = struct{}{}

func (server *Server) sendResponse(sending *sync.Mutex, req *Request, reply interface{}, codec ServerCodec, callErr error) {
	resp := server.getResponse()
	// Encode the response header
	resp.ServiceMethod = req.ServiceMethod
	if callErr != nil {
		resp.Error = callErr.Error()
		reply = invalidRequest
	}
	resp.Seq = req.Seq
	sending.Lock()
	err := codec.WriteResponse(resp, reply)
	if debugLog && err != nil {
		log.Println("rpc: writing response:", err)
	}
	sending.Unlock()
	server.freeResponse(resp)
}

func (m *methodType) NumCalls() (n uint) {
	m.Lock()
	n = m.numCalls
	m.Unlock()
	return n
}

func (s *service) call(ctx context.Context, server *Server, sending *sync.Mutex, wg *sync.WaitGroup, mtype *methodType, req *Request, argv, replyv reflect.Value, codec ServerCodec) error {
	if wg != nil {
		defer wg.Done()
	}
	mtype.Lock()
	mtype.numCalls++
	mtype.Unlock()
	function := mtype.method.Func

	callErr := callServiceMethod(ctx, mtype.HasContext, function, s.rcvr, argv, replyv)

	server.sendResponse(sending, req, replyv.Interface(), codec, callErr)
	server.freeRequest(req)
	return callErr
}

type gobServerCodec struct {
	conn   net.Conn
	dec    *gob.Decoder
	enc    *gob.Encoder
	encBuf *bufio.Writer
	closed bool
}

func (c *gobServerCodec) ReadRequestHeader(r *Request) error {
	return c.dec.Decode(r)
}

func (c *gobServerCodec) ReadRequestBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *gobServerCodec) WriteResponse(r *Response, body interface{}) (err error) {
	if err = c.enc.Encode(r); err != nil {
		if c.encBuf.Flush() == nil {
			// Gob couldn't encode the header. Should not happen, so if it does,
			// shut down the connection to signal that the connection is broken.
			log.Println("rpc: gob error encoding response:", err)
			c.Close()
		}
		return
	}
	if err = c.enc.Encode(body); err != nil {
		if c.encBuf.Flush() == nil {
			// Was a gob problem encoding the body but the header has been written.
			// Shut down the connection to signal that the connection is broken.
			log.Println("rpc: gob error encoding body:", err)
			c.Close()
		}
		return
	}
	return c.encBuf.Flush()
}

func (c *gobServerCodec) SourceAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *gobServerCodec) Close() error {
	if c.closed {
		// Only call c.rwc.Close once; otherwise the semantics are undefined.
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// ServeRequest is like ServeCodec but synchronously serves a single request.
// It does not close the codec upon completion.
func (server *Server) ServeRequest(codec ServerCodec) error {
	return server.ServeRequestContext(context.Background(), codec)
}

func (server *Server) ServeRequestContext(ctx context.Context, codec ServerCodec) error {
	sending := new(sync.Mutex)
	service, mtype, req, argv, replyv, keepReading, err := server.readRequest(codec)
	if err != nil {
		if !keepReading {
			return err
		}
		// send a response if we actually managed to read a header.
		if req != nil {
			server.sendResponse(sending, req, invalidRequest, codec, err)
			server.freeRequest(req)
		}
		return err
	}

	handler := func() error {
		return service.call(ctx, server, sending, nil, mtype, req, argv, replyv, codec)
	}

	if server.serverServiceCallInterceptor != nil {
		server.serverServiceCallInterceptor(req.ServiceMethod, argv, replyv, handler)
	} else {
		// service.call errors are sent to the client, not returned to the caller
		_ = handler()
	}

	return nil
}

func (server *Server) getRequest() *Request {
	server.reqLock.Lock()
	req := server.freeReq
	if req == nil {
		req = new(Request)
	} else {
		server.freeReq = req.next
		*req = Request{}
	}
	server.reqLock.Unlock()
	return req
}

func (server *Server) freeRequest(req *Request) {
	server.reqLock.Lock()
	req.next = server.freeReq
	server.freeReq = req
	server.reqLock.Unlock()
}

func (server *Server) getResponse() *Response {
	server.respLock.Lock()
	resp := server.freeResp
	if resp == nil {
		resp = new(Response)
	} else {
		server.freeResp = resp.next
		*resp = Response{}
	}
	server.respLock.Unlock()
	return resp
}

func (server *Server) freeResponse(resp *Response) {
	server.respLock.Lock()
	resp.next = server.freeResp
	server.freeResp = resp
	server.respLock.Unlock()
}

func (server *Server) readRequest(codec ServerCodec) (service *service, mtype *methodType, req *Request, argv, replyv reflect.Value, keepReading bool, err error) {
	service, mtype, req, keepReading, err = server.readRequestHeader(codec)
	if err != nil {
		if !keepReading {
			return
		}
		// discard body
		codec.ReadRequestBody(nil)
		return
	}

	if server.preBodyInterceptor != nil {
		// Allow interceptor to halt servicing of the request
		err = server.preBodyInterceptor(req.ServiceMethod, codec.SourceAddr())
		if err != nil {
			return
		}
	}

	// Decode the argument value.
	argv, argIsValue := interpretArgumentValue(mtype.ArgType)
	// argv guaranteed to be a pointer now.
	if err = codec.ReadRequestBody(argv.Interface()); err != nil {
		return
	}
	if argIsValue {
		argv = argv.Elem()
	}

	replyv = interpretReplyValue(mtype.ReplyType)
	return
}

func (server *Server) readRequestHeader(codec ServerCodec) (svc *service, mtype *methodType, req *Request, keepReading bool, err error) {
	// Grab the request header.
	req = server.getRequest()
	err = codec.ReadRequestHeader(req)
	if err != nil {
		req = nil
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return
		}
		err = errors.New("rpc: server cannot decode request: " + err.Error())
		return
	}

	// We read the header successfully. If we see an error now,
	// we can still recover and move on to the next request.
	keepReading = true

	svc, mtype, err = server.findMethod(req.ServiceMethod)

	return
}

func (server *Server) findMethod(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc: service/method request ill-formed: " + serviceMethod)
		return
	}

	serviceName := serviceMethod[:dot]
	methodName := serviceMethod[dot+1:]

	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc: can't find service " + serviceMethod)
		return
	}
	svc = svci.(*service)

	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc: can't find method " + serviceMethod)
	}
	return
}

func (server *Server) InvokeMethod(
	ctx context.Context,
	serviceMethod string,
	decodeArgFn func(any) error,
	sourceAddr net.Addr,
) (reflect.Value, error) {
	svc, mtype, err := server.findMethod(serviceMethod)
	if err != nil {
		return reflect.Value{}, err
	}

	if server.preBodyInterceptor != nil {
		// Allow interceptor to halt servicing of the request
		err = server.preBodyInterceptor(serviceMethod, sourceAddr)
		if err != nil {
			return reflect.Value{}, err
		}
	}

	argv, argIsValue := interpretArgumentValue(mtype.ArgType)
	argvPtr := argv.Interface()

	if err := decodeArgFn(argvPtr); err != nil {
		return reflect.Value{}, err
	}

	if argIsValue {
		argv = argv.Elem()
	}

	replyv := interpretReplyValue(mtype.ReplyType)

	function := mtype.method.Func

	// Capture the error so we can directly return it.
	var callErr error
	handler := func() error {
		callErr = callServiceMethod(ctx, mtype.HasContext, function, svc.rcvr, argv, replyv)
		return callErr
	}

	if server.serverServiceCallInterceptor != nil {
		server.serverServiceCallInterceptor(serviceMethod, argv, replyv, handler)
	} else {
		_ = handler()
	}

	if callErr != nil {
		return reflect.Value{}, callErr
	}

	return replyv, nil
}

func interpretArgumentValue(argType reflect.Type) (reflect.Value, bool) {
	var (
		argv       reflect.Value
		argIsValue bool // if true, need to indirect before calling.
	)
	if argType.Kind() == reflect.Ptr {
		argv = reflect.New(argType.Elem())
	} else {
		argv = reflect.New(argType)
		argIsValue = true
	}

	return argv, argIsValue
}

func interpretReplyValue(replyType reflect.Type) reflect.Value {
	replyv := reflect.New(replyType.Elem())

	switch replyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(replyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(replyType.Elem(), 0, 0))
	}

	return replyv
}

func callServiceMethod(ctx context.Context, useCtx bool, function, rcvr, argv, replyv reflect.Value) error {
	// Invoke the method, providing a new value for the reply.
	var args []reflect.Value
	if useCtx {
		args = []reflect.Value{rcvr, reflect.ValueOf(ctx), argv, replyv}
	} else {
		args = []reflect.Value{rcvr, argv, replyv}
	}
	returnValues := function.Call(args)

	// The return value for the method is an error.
	errInter := returnValues[0].Interface()
	if errInter != nil {
		return errInter.(error)
	}
	return nil
}

// A ServerCodec implements reading of RPC requests and writing of
// RPC responses for the server side of an RPC session.
// The server calls ReadRequestHeader and ReadRequestBody in pairs
// to read requests from the connection, and it calls WriteResponse to
// write a response back. The server calls Close when finished with the
// connection. ReadRequestBody may be called with a nil
// argument to force the body of the request to be read and discarded.
// See NewClient's comment for information about concurrent access.
type ServerCodec interface {
	ReadRequestHeader(*Request) error
	ReadRequestBody(interface{}) error
	WriteResponse(*Response, interface{}) error
	SourceAddr() net.Addr

	// Close can be called multiple times and must be idempotent.
	Close() error
}

// Can connect to RPC service using HTTP CONNECT to rpcPath.
var connected = "200 Connected to Go RPC"
