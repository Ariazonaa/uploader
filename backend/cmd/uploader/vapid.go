package main

import (
	"fmt"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// runVAPID generates a VAPID keypair for Web Push and prints it as env vars.
func runVAPID() error {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	fmt.Println("# Add these to your .env to enable browser push:")
	fmt.Printf("UPLOADER_VAPID_PUBLIC_KEY=%s\n", pub)
	fmt.Printf("UPLOADER_VAPID_PRIVATE_KEY=%s\n", priv)
	fmt.Println("UPLOADER_VAPID_SUBJECT=mailto:you@example.com")
	return nil
}
