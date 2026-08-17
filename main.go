package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const (
	rpcURL   = "http://localhost:8546"
	block    = uint64(25690000)
	pageSize = 50

	contractsFile        = "contracts.jsonl"
	contractsStorageFile = "contracts_storage.jsonl"
	checkpointFile       = "account_scan.checkpoint.json"

	// How often to print the summary and save the checkpoint.
	summaryInterval = 30 * time.Second

	// Flush the JSONL writer after this many contracts.
	flushEvery = 100

	emptyCodeHash = "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
)

type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type AccountRangeResult struct {
	Root     string             `json:"root"`
	Accounts map[string]Account `json:"accounts"`
	Next     string             `json:"next"`
}

type Account struct {
	Balance  string `json:"balance"`
	Nonce    uint64 `json:"nonce"`
	Root     string `json:"root"`
	CodeHash string `json:"codeHash"`
	Address  string `json:"address"`
	Key      string `json:"key"`
	Code     string `json:"code,omitempty"`
}

type ContractInfo struct {
	Address  string `json:"address"`
	CodeSize int    `json:"codeSize"`
}

type ContractStorageInfo struct {
	Address     string `json:"address"`
	CodeSize    int    `json:"codeSize"`
	NumKeys     int    `json:"numKeys"`
	StorageSize int    `json:"storageSize"`
}

type Checkpoint struct {
	Block            uint64 `json:"block"`
	Next             string `json:"next"`
	RegularAccounts  uint64 `json:"regularAccounts"`
	ContractAccounts uint64 `json:"contractAccounts"`
	AccountsSeen     uint64 `json:"accountsSeen"`
	PagesProcessed   uint64 `json:"pagesProcessed"`
	Completed        bool   `json:"completed"`
}

type Counters struct {
	regularAccounts  atomic.Uint64
	contractAccounts atomic.Uint64
	accountsSeen     atomic.Uint64
	pagesProcessed   atomic.Uint64
	bytesWritten     atomic.Uint64
}

var requestID atomic.Uint64

func rpcCall(method string, params any, result any) error {
	id := requestID.Add(1)

	reqBody := RPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      int(id),
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := http.Post(
		rpcURL,
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return fmt.Errorf(
			"RPC HTTP error: %s: %s",
			resp.Status,
			string(body),
		)
	}

	var rpcResp RPCResponse

	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}

	if rpcResp.Error != nil {
		return fmt.Errorf(
			"RPC error %d: %s",
			rpcResp.Error.Code,
			rpcResp.Error.Message,
		)
	}

	if err := json.Unmarshal(rpcResp.Result, result); err != nil {
		return err
	}

	return nil
}

func getAccountPage(start []int) (*AccountRangeResult, error) {
	var result AccountRangeResult

	if len(start) == 0 {
		// Make an empty slice to avoid sending null in the JSON-RPC request.
		start = make([]int, 0)
	}

	params := []any{
		block,
		start,
		pageSize,
		false, // with code
		true,  // no storage
		false, // incompletes
	}

	if err := rpcCall(
		"debug_accountRange",
		params,
		&result,
	); err != nil {
		return nil, err
	}

	return &result, nil
}

func loadCheckpoint() (*Checkpoint, error) {
	data, err := os.ReadFile(checkpointFile)

	if os.IsNotExist(err) {
		return &Checkpoint{
			Block: block,
		}, nil
	}

	if err != nil {
		return nil, err
	}

	var checkpoint Checkpoint

	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, err
	}

	if checkpoint.Block != block {
		return nil, fmt.Errorf(
			"checkpoint is for block %d, requested block is %d",
			checkpoint.Block,
			block,
		)
	}

	fmt.Printf(
		"Loaded checkpoint: next=%s regular=%d contracts=%d accountsSeen=%d pagesProcessed=%d completed=%v\n",
		checkpoint.Next,
		checkpoint.RegularAccounts,
		checkpoint.ContractAccounts,
		checkpoint.AccountsSeen,
		checkpoint.PagesProcessed,
		checkpoint.Completed,
	)

	return &checkpoint, nil
}

func saveCheckpoint(cp *Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := checkpointFile + ".tmp"

	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpFile, checkpointFile)
}

func printSummary(c *Counters, startTime time.Time) {
	elapsed := time.Since(startTime)

	accounts := c.accountsSeen.Load()
	regular := c.regularAccounts.Load()
	contracts := c.contractAccounts.Load()
	pages := c.pagesProcessed.Load()
	bytes := c.bytesWritten.Load()

	var rate float64

	if elapsed.Seconds() > 0 {
		rate = float64(accounts) / elapsed.Seconds()
	}

	fmt.Printf(
		"[%s] accounts=%d regular=%d contracts=%d pages=%d rate=%.1f accounts/s output=%d MB elapsed=%s\n",
		time.Now().Format("2006-01-02 15:04:05"),
		accounts,
		regular,
		contracts,
		pages,
		rate,
		bytes/(1024*1024),
		elapsed.Round(time.Second),
	)
}

func decodeCursor(cursor string) ([]int, error) {
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, err
	}

	start := make([]int, len(raw))
	for i, b := range raw {
		start[i] = int(b)
	}

	return start, nil
}

func main() {
	shouldReturn := fetchAccounts()
	if shouldReturn {
		return
	}
}

func fetchAccounts() bool {
	checkpoint, err := loadCheckpoint()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load checkpoint: %v\n", err)
		os.Exit(1)
	}

	if checkpoint.Completed {
		fmt.Println("Scan is already marked as completed.")
		fmt.Printf(
			"Regular accounts: %d\nContract accounts: %d\n",
			checkpoint.RegularAccounts,
			checkpoint.ContractAccounts,
		)
		return true
	}

	// Append-only output.
	file, err := os.OpenFile(
		contractsFile,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open output file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	writer := bufio.NewWriterSize(file, 1024*1024)

	var counters Counters

	counters.regularAccounts.Store(checkpoint.RegularAccounts)

	counters.contractAccounts.Store(checkpoint.ContractAccounts)

	counters.accountsSeen.Store(checkpoint.AccountsSeen)

	counters.pagesProcessed.Store(checkpoint.PagesProcessed)

	startTime := time.Now()

	// Resume from checkpoint cursor.
	var start []int

	if checkpoint.Next != "" {
		start, err = decodeCursor(checkpoint.Next)
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"failed to decode checkpoint cursor: %v\n",
				err,
			)
			os.Exit(1)
		}
	}

	lastSummary := time.Now()
	contractsSinceFlush := 0

	for {
		page, err := getAccountPage(start)
		if err != nil {
			_ = writer.Flush()
			_ = file.Sync()

			cp := Checkpoint{
				Block:            block,
				Next:             checkpoint.Next,
				RegularAccounts:  counters.regularAccounts.Load(),
				ContractAccounts: counters.contractAccounts.Load(),
				AccountsSeen:     counters.accountsSeen.Load(),
				PagesProcessed:   counters.pagesProcessed.Load(),
				Completed:        false,
			}

			_ = saveCheckpoint(&cp)

			fmt.Fprintf(
				os.Stderr,
				"debug_accountRange failed: %v\n",
				err,
			)

			os.Exit(1)
		}

		counters.pagesProcessed.Add(1)

		counters.accountsSeen.Add(uint64(len(page.Accounts)))

		for address, account := range page.Accounts {

			// No code means regular account.
			if account.CodeHash == emptyCodeHash ||
				account.Code == "" ||
				account.Code == "0x" {

				counters.regularAccounts.Add(1)

				continue
			}

			counters.contractAccounts.Add(1)

			// "0x" + two hex characters per byte.
			codeSize := (len(account.Code) - 2) / 2

			// A Keccak-256 hash is normally 32 bytes.
			codeHashSize := (len(account.CodeHash) - 2) / 2

			info := ContractInfo{
				Address:  address,
				CodeSize: codeSize + codeHashSize,
			}

			line, err := json.Marshal(info)
			if err != nil {
				fmt.Fprintf(
					os.Stderr,
					"failed to encode contract %s: %v\n",
					address,
					err,
				)
				os.Exit(1)
			}

			line = append(line, '\n')

			n, err := writer.Write(line)
			if err != nil {
				fmt.Fprintf(
					os.Stderr,
					"failed to write contract: %v\n",
					err,
				)
				os.Exit(1)
			}

			counters.bytesWritten.Add(uint64(n))

			contractsSinceFlush++

			if contractsSinceFlush >= flushEvery {
				if err := writer.Flush(); err != nil {
					fmt.Fprintf(
						os.Stderr,
						"failed to flush output: %v\n",
						err,
					)
					os.Exit(1)
				}

				contractsSinceFlush = 0
			}
		}

		// Update cursor only after the page has been completely
		// processed and its contract records have been written.
		checkpoint.Next = page.Next

		now := time.Now()

		if now.Sub(lastSummary) >= summaryInterval {
			if err := writer.Flush(); err != nil {
				fmt.Fprintf(
					os.Stderr,
					"failed to flush output: %v\n",
					err,
				)
				os.Exit(1)
			}

			if err := file.Sync(); err != nil {
				fmt.Fprintf(
					os.Stderr,
					"failed to sync output: %v\n",
					err,
				)
				os.Exit(1)
			}

			cp := Checkpoint{
				Block:            block,
				Next:             checkpoint.Next,
				RegularAccounts:  counters.regularAccounts.Load(),
				ContractAccounts: counters.contractAccounts.Load(),
				AccountsSeen:     counters.accountsSeen.Load(),
				PagesProcessed:   counters.pagesProcessed.Load(),
				Completed:        false,
			}

			if err := saveCheckpoint(&cp); err != nil {
				fmt.Fprintf(
					os.Stderr,
					"failed to save checkpoint: %v\n",
					err,
				)
				os.Exit(1)
			}

			printSummary(&counters, startTime)

			lastSummary = now
		}

		// Finished.
		if page.Next == "" {
			break
		}

		next, err := base64.StdEncoding.DecodeString(page.Next)
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"failed to decode next cursor: %v\n",
				err,
			)
			os.Exit(1)
		}

		if len(next) == 0 {
			break
		}

		start, err = decodeCursor(page.Next)
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"failed to decode next cursor: %v\n",
				err,
			)
			os.Exit(1)
		}
	}

	// Final flush.
	if err := writer.Flush(); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"failed to flush output: %v\n",
			err,
		)
		os.Exit(1)
	}

	if err := file.Sync(); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"failed to sync output: %v\n",
			err,
		)
		os.Exit(1)
	}

	// Final checkpoint.
	finalCheckpoint := Checkpoint{
		Block:            block,
		Next:             "",
		RegularAccounts:  counters.regularAccounts.Load(),
		ContractAccounts: counters.contractAccounts.Load(),
		AccountsSeen:     counters.accountsSeen.Load(),
		PagesProcessed:   counters.pagesProcessed.Load(),
		Completed:        true,
	}

	if err := saveCheckpoint(&finalCheckpoint); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"failed to save final checkpoint: %v\n",
			err,
		)
		os.Exit(1)
	}

	printSummary(&counters, startTime)
	return false
}
