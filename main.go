package main

import (
	"bufio"
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"log"
	"os"
	"regexp"
	"strconv"
	"sync"
)

const (
	MDC_ST_LOG_FILEPATH = "./mdc_stocktake.log"
	MDC_ST_DB_FILEPATH  = "./mdc_inventory.db"
	MDC_ST_LOC_PATTERN  = "^[A-W]\\d{1,2}$"
	MDC_ST_SOH_PATTERN  = "^\\d{1,3}$"

	MDC_ST_DB_QUERY = "INSERT INTO inventory (item_location, item_code, item_soh) VALUES (?, ?, ?) ON CONFLICT(item_location, item_code) DO UPDATE SET item_soh = item_soh + ?"

	MDC_ST_CLI_PROMPT = "MDC_ST $"
)

type transaction struct {
	location string
	code     string
	soh      int
}

var (
	END_OF_TRANSACTIONS = transaction{
		"",
		"",
		-1,
	}
)

type Context struct {
	ctx_dbconn  *sql.DB
	ctx_running bool

	ctx_logfile *os.File

	ctx_loc_finder *regexp.Regexp
	ctx_soh_finder *regexp.Regexp

	ctx_current_loc  string
	ctx_current_code string
	ctx_history      []*transaction

	ctx_transaction_chan chan *transaction
	ctx_dbwait           *sync.WaitGroup
}

func GenContext() Context {
	ctx := Context{}

	ctx.ctx_current_loc = ""
	ctx.ctx_current_code = ""
	ctx.ctx_history = make([]*transaction, 0)
	ctx.ctx_transaction_chan = make(chan *transaction)
	ctx.ctx_running = true
	ctx.ctx_dbwait = &sync.WaitGroup{}

	logfile, err := os.OpenFile(MDC_ST_LOG_FILEPATH, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	log.SetOutput(logfile)

	db, err := sql.Open("sqlite3", MDC_ST_DB_FILEPATH)
	if err != nil {
		log.Fatalf("could not open database file \"%s\".", MDC_ST_DB_FILEPATH)
	}
	ctx.ctx_dbconn = db

	r, err := regexp.Compile(MDC_ST_LOC_PATTERN)
	if err != nil {
		log.Fatalf("could not compile regex for location \"%s\".", MDC_ST_LOC_PATTERN)
	}
	ctx.ctx_loc_finder = r

	r, err = regexp.Compile(MDC_ST_SOH_PATTERN)
	if err != nil {
		log.Fatalf("could not compile regex for soh \"%s\".", MDC_ST_SOH_PATTERN)
	}
	ctx.ctx_soh_finder = r

	return ctx
}

func SubmitTransaction(ctx *Context, loc string, code string, soh int) {
	count := &transaction{
		loc,
		code,
		soh,
	}

	log.Printf("[INFO] Submit count to database (%s, %s, %d)", loc, code, soh)
	fmt.Printf("[INFO] Submit count to database (%s, %s, %d)\n", loc, code, soh)
	ctx.ctx_history = append(ctx.ctx_history, count)
	ctx.ctx_current_code = ""

	ctx.ctx_transaction_chan <- count
}

func UndoTransaction(ctx *Context) {
	var c *transaction
	c, ctx.ctx_history = ctx.ctx_history[len(ctx.ctx_history)-1], ctx.ctx_history[:len(ctx.ctx_history)-1]

	log.Printf("[INFO] Reverting transaction (%s, %s, %d)", c.location, c.code, c.soh)
	fmt.Printf("[INFO] Reverting transaction (%s, %s, %d)\n", c.location, c.code, c.soh)
	SubmitTransaction(ctx, c.location, c.code, -c.soh)
}

func StoreTransactions(ctx *Context) {
	ctx.ctx_dbwait.Add(1)

	tx, err := ctx.ctx_dbconn.Begin()
	if err != nil {
		log.Print(err)
		return
	}

	add_stmt, err := tx.Prepare(MDC_ST_DB_QUERY)
	if err != nil {
		log.Print(err)
		return
	}

	for count := range ctx.ctx_transaction_chan {
		if *count == END_OF_TRANSACTIONS {
			log.Print("[INFO] Got end of transactions, closing connection to database")
			break
		}

		_, err = add_stmt.Exec(count.location, count.code, count.soh, count.soh)
		if err != nil {
			log.Print(err)
			continue
		}
	}

	err = tx.Commit()
	if err != nil {
		log.Print(err)
	}

	ctx.ctx_dbwait.Done()
}

func DestroyContext(ctx *Context) {
	if ctx == nil {
		return
	}

	log.Print("[INFO] Destroying context, sending end of transactions...")
	ctx.ctx_transaction_chan <- &END_OF_TRANSACTIONS
	ctx.ctx_dbwait.Wait()
	close(ctx.ctx_transaction_chan)

	ctx.ctx_logfile.Close()
}

func ProcessInput(ctx *Context, user_input string) bool {
	if user_input == "exit" {
		if ctx.ctx_current_loc != "" && ctx.ctx_current_code != "" {
			SubmitTransaction(ctx, ctx.ctx_current_loc, ctx.ctx_current_code, 1)
		}
		return true
	} else if user_input == "undo" {
		if len(ctx.ctx_history) != 0 {
			UndoTransaction(ctx)
		} else {
			log.Print("[INFO] No more transactions to revert")
			println("[INFO] No more transactions to revert")
		}
		return false
	}

	if ctx.ctx_loc_finder.MatchString(user_input) {
		if ctx.ctx_current_loc != "" && ctx.ctx_current_code != "" {
			SubmitTransaction(ctx, ctx.ctx_current_loc, ctx.ctx_current_code, 1)
		}
		log.Printf("[INFO] Location changed from \"%s\" to \"%s\"", ctx.ctx_current_loc, user_input)
		fmt.Printf("[INFO] Location changed from \"%s\" to \"%s\"\n", ctx.ctx_current_loc, user_input)
		ctx.ctx_current_loc = user_input
		return false
	} else if ctx.ctx_soh_finder.MatchString(user_input) {
		if ctx.ctx_current_loc == "" {
			log.Print("[ERROR] You need to set a location before providing a quantity")
			println("[ERROR] You need to set a location before providing a quantity")
			return false
		}

		if ctx.ctx_current_code == "" {
			log.Print("[ERROR] You need to provide an item code before providing a quantity")
			println("[ERROR] You need to provide an item code before providing a quantity")
			return false
		}

		i, _ := strconv.Atoi(user_input) // FIXME: we should care about errors
		SubmitTransaction(ctx, ctx.ctx_current_loc, ctx.ctx_current_code, i)
		return false
	} else {
		if ctx.ctx_current_loc == "" {
			log.Print("[ERROR] You need to provide a location before providing an item code.")
			println("[ERROR] You need to provide a location before providing an item code.")
			return false
		}

		if ctx.ctx_current_code != "" {
			SubmitTransaction(ctx, ctx.ctx_current_loc, ctx.ctx_current_code, 1)
		}

		ctx.ctx_current_code = user_input
		return false
	}
}

func main() {
	ctx := GenContext()
	scanner := bufio.NewScanner(os.Stdin)

	go StoreTransactions(&ctx)
	defer DestroyContext(&ctx)

	for ctx.ctx_running {
		print(MDC_ST_CLI_PROMPT)
		scanner.Scan()

		if scanner.Err() != nil {
			log.Fatal("could not take user input.")
		}

		if ProcessInput(&ctx, scanner.Text()) {
			break
		}
	}

	println("[INFO] Closing stocktake, your data is safe :^)")
}
