// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// MailProcessor is the hook of the mail comfort features (implemented by the
// app, internal/app/comfort_*.go). ProcessNewMail runs once for every newly
// stored message whose body was downloaded (before mail.received is
// published); EnrichMailEvent adds the analysis (category, spam score,
// e-invoice) to every mail event built by the sync engine. Both must be
// fast and must not call back into the sync engine synchronously.
type MailProcessor interface {
	ProcessNewMail(ctx context.Context, msg store.Message, raw []byte, role domain.FolderRole)
	EnrichMailEvent(ctx context.Context, msg store.Message, mail *events.Mail)
}
