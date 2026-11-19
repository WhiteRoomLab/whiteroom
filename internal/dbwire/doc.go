// Package dbwire is the protocol between the DB daemon (internal/dbserver, cmd/whiteroom-db) and its clients
// (internal/dbclient, internal/store), concept section 3.6, with what both sides know of the tables and of the events
// that the daemon adds.
//
// A Hello comes first on a connection. The daemon answers the requests of a connection in their order, and a client
// reads the reply to a request before it sends the next, except for a window of reads (WindowRequests). A message is
// one or more frames of one kind, the last with the end flag, and its body is the payloads of its frames joined. A frame
// is a header of 8 bytes and its payload: the kind, the flags, two reserved bytes that are zero, and the length of the
// payload as a uint32 in big-endian. Only the flag 0x01, the end, is defined. A reply is a message of the kind OK, or
// one Error frame, which also ends an OK message that has begun. In a reply, an empty OK frame without the end flag is a
// pulse, which says that a long request goes on; a Reader skips it, and it has no place in a request.
//
// The integers of a body have a fixed width and are big-endian. A byte string is its length as a uint32 and its bytes.
// An Enc writes a body and a Dec reads one; both keep their first error. The write methods of Enc and the read
// functions of this package come in pairs, named for the message: a request or a reply, and for a list of elements
// (rows, events, operations, backup entries) one write method and one read function per element. A read function that
// reads the end of a body also checks, with Dec.End, that nothing follows; the others leave that to the caller.
// Messages whose body is empty are read with ReadEmpty.
//
// Every refusal of this package wraps ErrProtocol, and a size over a limit also wraps ErrLimit. No error text holds
// bytes of a payload: only lengths, kinds, codes and field names.
package dbwire
