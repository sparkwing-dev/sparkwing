# Local controller transport

`DialSocket` and `ListenSocket` own the local transport for admission and the
controller API. `SocketPath` and `APISocketPath` select stable home-bound endpoint
keys. Unix uses those keys as socket paths. Windows hashes the normalized key
into a local named-pipe name. Environment variables and request fields cannot
select a peer identity.

`wingd/client.NewAPIClient` carries HTTP to `APISocketPath` through `DialSocket`
on every platform. `APIBaseURL` supplies the HTTP request origin. The transport
ignores request network addresses, consults no proxy and follows no redirects.
It preserves response statuses and bodies and performs no application retries.
Callers supply it as `EngineOptions.HTTP` and `APIBaseURL` as `EngineOptions.URL`;
the shared clients' route policy owns any retry. Close idle connections when finished.

`ListenSocket` yields `APIConn` only after checking the kernel identity against
the controller account. Unix supplies the socket peer UID. Windows reads one
transport preamble byte, obtains the pipe client's identification token and
checks its SID. Anonymous tokens, malformed preambles and missing credentials
fail closed. Successful authentication consumes and acknowledges the preamble
before application traffic starts. `APIConn.PeerIdentity` returns a snapshot.

`LocalController.HTTP` returns the shared API and dashboard handlers. The local
runtime serves them through `LocalHTTP`, which carries `APIConn` identity in
each API request context. The route authorizer reads `LocalPeer(request.Context())`.
Request headers and remote addresses cannot replace that identity. Dashboard
handlers retain their browser authentication. Shutdown cancels request contexts,
uses `APIDrainWindow` for graceful drain, closes remaining HTTP connections and
joins handlers before the runtime closes its store. `TestLocalHTTP` and
`FuzzLocalHTTPIdentity` cover this composition with in-memory connections.

The runtime counts accepted dashboard connections until close, including
keep-alive and hijacked connections. Repeated or concurrent closes release one
hold. These counts join the shared controller's activity without replacing its
run, queue or lease counts. API connections add no dashboard hold.
`TestLocalRuntimeDashboardConnectionsHoldIdle` exercises the serving loop,
status, idle timer and joined shutdown over in-memory connections.

Windows pipes have a protected owner-only DACL, reject remote clients and use
first-instance ownership. Clients check the pipe owner before sending the
preamble and request identification-level impersonation. The server reads the
token on a dedicated locked OS thread. A thread that cannot revert exits with
its goroutine. The implementation follows Microsoft's
[pipe security](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)
and [client impersonation](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-impersonatenamedpipeclient)
contracts.

Caller cancellation bounds connection and handshake work. The server uses
`limits.LocalControllerHandshake`. Listener closure cancels and joins pending
connects and authentication, while authenticated connections survive for drain.
The runtime retains its existing home election until admission and API drains
finish. A Windows replacement binds after those accepted connections close.

The Windows tests cover SID delivery, owner-only DACLs, exclusive binding,
cancellation, incomplete authentication, immediate rebinding after shutdown and
authenticated connection survival. The property test varies the preamble,
anonymous token and forged SID bytes. Native Windows execution and a distinct
account's connection remain required acceptance evidence. Cross-compilation
does not satisfy `credentials-windows-local-subject`.
