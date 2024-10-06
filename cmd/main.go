package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	lnonion "github.com/elnosh/lightning-onion-routing"
	"github.com/urfave/cli/v2"
)

const (
	BOB     = "71df4af67d0236f148e8c4d764ead3662693b4561b7bca19c6c7b3d804098fee"
	CHARLIE = "3aae4a7a4717e9721b49e8247be4a1280c2d9afad9f011dedc9e3650051c9ae9"
	DAVE    = "34df19f85e920cb3a0dd529fd61dace4ac9a567c00c521b98e75762eed06911b"
)

var (
	bob     *secp256k1.PrivateKey
	charlie *secp256k1.PrivateKey
	dave    *secp256k1.PrivateKey
)

func setupKeys(ctx *cli.Context) error {
	keybytes, _ := hex.DecodeString(BOB)
	bob = secp256k1.PrivKeyFromBytes(keybytes)

	keybytes, _ = hex.DecodeString(CHARLIE)
	charlie = secp256k1.PrivKeyFromBytes(keybytes)

	keybytes, _ = hex.DecodeString(DAVE)
	dave = secp256k1.PrivKeyFromBytes(keybytes)

	return nil
}

func main() {
	app := cli.App{
		Name: "lnonion",
		Commands: []*cli.Command{
			onionCmd,
			parseCmd,
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

var onionCmd = &cli.Command{
	Name:   "onion",
	Usage:  "build onion",
	Before: setupKeys,
	Action: buildOnion,
}

func buildOnion(ctx *cli.Context) error {
	sessionKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return err
	}

	fmt.Println("start building the onion. What payload do you want to put for Bob:")

	reader := bufio.NewReader(os.Stdin)
	bobPayload, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("could not read input: %v", err)
	}

	fmt.Println("What payload do you want to put for Charlie (2nd hop):")
	charliePayload, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("could not read input: %v", err)
	}

	fmt.Println("What payload do you want to put for Dave (last hop):")
	davePayload, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("could not read input: %v", err)
	}

	hops := []lnonion.HopPayload{
		{PublicKey: bob.PubKey(), Payload: []byte(bobPayload)},
		{PublicKey: charlie.PubKey(), Payload: []byte(charliePayload)},
		{PublicKey: dave.PubKey(), Payload: []byte(davePayload)},
	}

	onion, err := lnonion.ConstructOnion(sessionKey, hops)
	if err != nil {
		return err
	}

	fmt.Printf("onion to pass to first hop (bob): %x\n", onion.Serialize())

	return nil
}

var parseCmd = &cli.Command{
	Name:      "parse",
	Usage:     "parse onion",
	ArgsUsage: "[ONION]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "hop",
			Usage: "specify hop (bob, charlie or dave) from which to parse onion",
		},
	},
	Before: setupKeys,
	Action: parseOnion,
}

func parseOnion(ctx *cli.Context) error {
	args := ctx.Args()
	if args.Len() < 1 {
		return errors.New("pass an onion to parse")
	}

	hop := ctx.String("hop")

	var hopKey *secp256k1.PrivateKey
	switch hop {
	case "bob":
		hopKey = bob
	case "charlie":
		hopKey = charlie
	case "dave":
		hopKey = dave
	default:
		return errors.New("invalid hop")
	}

	onionBytes, err := hex.DecodeString(args.First())
	if err != nil {
		return fmt.Errorf("error decoding onion: %v", err)
	}

	onion, err := lnonion.DeserializeOnion(onionBytes)
	if err != nil {
		return err
	}

	payloadForHop, onionToForward, err := lnonion.ProcessOnion(onion, hopKey)
	if errors.Is(err, lnonion.FinalHop) {
		fmt.Printf("payload for %v: %s\n", hop, payloadForHop.Payload)
		fmt.Println("this is the onion's final destination")
		return nil
	} else if err != nil {
		return err
	}

	// TODO: print obfuscated part
	fmt.Printf("payload for %v: %s\n", hop, payloadForHop.Payload)
	fmt.Printf("onion for the next hop: %x\n", onionToForward.Serialize())

	return nil
}
