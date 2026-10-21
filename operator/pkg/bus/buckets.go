/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

// DiscussStream is the JetStream stream that holds every discussion subject.
const DiscussStream = "KUBEMOOT_DISCUSS"

// ServerTicketsBucket is the NATS KV bucket holding ServerTicket entries.
const ServerTicketsBucket = "kubemoot_server_tickets"

// ServerTicketPrefix starts the key of every ticket on the server; the key is
// <server>__<ticketId>.
func ServerTicketPrefix(server string) string { return server + "__" }
