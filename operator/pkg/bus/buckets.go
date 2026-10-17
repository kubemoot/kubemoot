/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

// ProviderTicketsBucket is the NATS KV bucket holding ProviderTicket entries.
const ProviderTicketsBucket = "kubemoot_provider_tickets"

// ProviderTicketPrefix starts the key of every ticket on the provider; the key is
// <provider>__<ticketId>.
func ProviderTicketPrefix(provider string) string { return provider + "__" }
