package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/blockstore"
	"github.com/filecoin-project/lotus/chain/actors/adt"
	"github.com/filecoin-project/lotus/chain/actors/builtin/multisig"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"
)

type MsigPendingTxn = struct {
	Id       int64             `json:"id"`
	To       string            `json:"to"`
	Value    big.Int           `json:"value"`
	Method   abi.MethodNum     `json:"method"`
	Params   string            `json:"params"`
	Approved []address.Address `json:"approved"`
}

var msigServePendingTxns = &cli.Command{
	Name:      "serve-pending-txns",
	Usage:     "Start a web server that serves the pending transaction information of multisig addresses",
	ArgsUsage: "[port]",
	Flags:     []cli.Flag{},
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return ShowHelp(cctx, fmt.Errorf("must specify address of multisig to inspect"))
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()
		ctx := ReqContext(cctx)

		store := adt.WrapStore(ctx, cbor.NewCborStore(blockstore.NewAPIBlockstore(api)))

		handler := func(addr string) ([]MsigPendingTxn, error) {
			maddr, err := address.NewFromString(addr)
			if err != nil {
				return []MsigPendingTxn{}, err
			}

			head, err := api.ChainHead(ctx)
			if err != nil {
				return []MsigPendingTxn{}, err
			}

			act, err := api.StateGetActor(ctx, maddr, head.Key())
			if err != nil {
				return []MsigPendingTxn{}, err
			}

			mstate, err := multisig.Load(store, act)
			if err != nil {
				return []MsigPendingTxn{}, err
			}

			pending := make(map[int64]multisig.Transaction)
			if err := mstate.ForEachPendingTxn(func(id int64, txn multisig.Transaction) error {
				pending[id] = txn
				return nil
			}); err != nil {
				return []MsigPendingTxn{}, xerrors.Errorf("reading pending transactions: %w", err)
			}

			var txids []int64
			for txid := range pending {
				txids = append(txids, txid)
			}
			sort.Slice(txids, func(i, j int) bool {
				return txids[i] < txids[j]
			})

			txns := []MsigPendingTxn{}

			for _, txid := range txids {
				tx := pending[txid]
				target := tx.To.String()
				paramStr := fmt.Sprintf("%x", tx.Params)

				txns = append(txns, MsigPendingTxn{
					Id:       txid,
					To:       target,
					Value:    tx.Value,
					Method:   tx.Method,
					Params:   paramStr,
					Approved: tx.Approved,
				})
			}

			return txns, nil
		}

		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			addr := r.URL.Path[1:]
			txns, err := handler(addr)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}

			jsonOut, err := json.Marshal(txns)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			w.Write(jsonOut)
		})

		port, err := strconv.ParseInt(cctx.Args().First(), 0, 32)
		if err != nil {
			return fmt.Errorf("port must be an integer: %s", err.Error())
		}

		c := make(chan os.Signal)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			os.Exit(1)
		}()

		listenAddr := fmt.Sprintf(":%d", port)
		fmt.Printf("Listening on %s\n", listenAddr)
		log.Fatal(http.ListenAndServe(listenAddr, nil))
		return nil
	},
}
