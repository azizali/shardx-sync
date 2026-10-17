package core

// Public endpoints of the team's Neon project. None of these are secrets:
// access is controlled by Neon Auth sign-in plus the team passphrase.
const (
	// Neon database URL over https (host + database). Auth and Data API URLs are derived from it.
	DefaultNeonURL = "https://ep-soft-sound-b4if536k.c-6.us-east-2.aws.neon.tech/neondb"
	// Neon Function that hands out presigned storage URLs (neon/storage-fn).
	DefaultStorageURL = "https://br-blue-wind-b4dkxg7d-shardxstorage.compute.c-6.us-east-2.aws.neon.tech"
	// Origin registered as a trusted domain in Neon Auth.
	DefaultAuthOrigin = "https://shardx-sync.app"
)
