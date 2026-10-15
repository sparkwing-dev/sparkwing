# Local controller transport

`DialSocket` and `ListenSocket` own the local transport for admission and the
controller API. `SocketPath` and `APISocketPath` select stable home-bound endpoint
keys. Unix uses those keys as socket paths. Windows hashes the normalized key
into a local named-pipe name. Environment variables and request fields cannot
select a peer identity.

`ListenSocket` yields `APIConn` only after checking the kernel identity against
the controller account. Unix supplies the socket peer UID. Windows reads one
transport preamble byte, obtains the pipe client's identification token and
checks its SID. Anonymous tokens, malformed preambles and missing credentials
fail closed. Successful authentication consumes and acknowledges the preamble
before application traffic starts. `APIConn.PeerIdentity` returns a snapshot.

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
