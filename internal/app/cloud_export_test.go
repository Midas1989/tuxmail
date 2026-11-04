// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "github.com/Midas1989/Mailclient/internal/workflow"

// WorkflowCloud exposes the workflow backends to the black-box tests.
func (a *App) WorkflowCloud() (workflow.CloudActions, workflow.PDFActions) {
	return workflowCloud{a: a}, workflowPDF{a: a}
}
