// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "os"

// EnvFCMServiceAccount overrides push.fcm.service_account_file.
const EnvFCMServiceAccount = "TUXMAIL_FCM_SERVICE_ACCOUNT"

// Push configures native push services in addition to Web Push (VAPID keys
// are generated automatically in data_dir/vapid.json).
type Push struct {
	FCM FCM `yaml:"fcm"`
}

// FCM configures Firebase Cloud Messaging for the Play Store variant of the
// Android app (KONZEPT 15.9). The service account needs the role
// "Firebase Cloud Messaging API Admin"; its project is the Firebase project
// of the app.
type FCM struct {
	// ServiceAccountFile is the path of the service account JSON key.
	ServiceAccountFile string `yaml:"service_account_file"`
}

// FCMServiceAccountFile returns the configured key file ("" = FCM disabled);
// the environment variable TUXMAIL_FCM_SERVICE_ACCOUNT takes precedence.
func (c *Config) FCMServiceAccountFile() string {
	if v := os.Getenv(EnvFCMServiceAccount); v != "" {
		return v
	}
	return c.Push.FCM.ServiceAccountFile
}
