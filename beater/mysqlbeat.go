package beater

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/beats/libbeat/beat"
	"github.com/elastic/beats/libbeat/cfgfile"
	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"

	"mysqlbeat/config"

	// mysql go driver
	"bufio"
	"encoding/json"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"regexp"
	"sync"
)

// Mysqlbeat is a struct to hold the beat config & info
type Mysqlbeat struct {
	beatConfig       *config.Config
	resumeFile       chan string
	mu               sync.Mutex
	done             chan struct{}
	period           time.Duration
	hostname         string
	port             string
	username         string
	password         string
	passwordAES      string
	queries          []string
	queryTypes       []string
	deltaWildcard    string
	deltaKeyWildcard string

	oldValues    common.MapStr
	oldValuesAge common.MapStr
}

type ResumeIndex struct {
	Index string `json:"index"`
	Value string `json:"value"`
}

var (
	commonIV = []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}
)

const (
	// secret length must be 16, 24 or 32, corresponding to the AES-128, AES-192 or AES-256 algorithms
	// you should compile your mysqlbeat with a unique secret and hide it (don't leave it in the code after compiled)
	// you can encrypt your password with github.com/adibendahan/mysqlbeat-password-encrypter just update your secret
	// (and commonIV if you choose to change it) and compile.
	secret = "github.com/adibendahan/mysqlbeat"

	// default values
	defaultPeriod           = "10s"
	defaultHostname         = "127.0.0.1"
	defaultPort             = "3306"
	defaultUsername         = "mysqlbeat_user"
	defaultPassword         = "mysqlbeat_pass"
	defaultDeltaWildcard    = "__DELTA"
	defaultDeltaKeyWildcard = "__DELTAKEY"

	// query types values
	queryTypeSingleRow          = "single-row"
	queryTypeMultipleRows       = "multiple-rows"
	queryTypeTwoColumns         = "two-columns"
	queryTypeSlaveDelay         = "show-slave-delay"
	queryTypeResumeMultipleRows = "resume-multiple-rows"

	resumeMultipleRowsFile = "resume-multiple-rows.db"

	// special column names values
	columnNameSlaveDelay = "Seconds_Behind_Master"

	// column types values
	columnTypeString = iota
	columnTypeInt
	columnTypeFloat
)

// New Creates beater
func New() *Mysqlbeat {
	return &Mysqlbeat{
		done:       make(chan struct{}),
		resumeFile: make(chan string),
	}
}

func (bt *Mysqlbeat) MockSetConfig(config *config.Config) {
	bt.beatConfig = config
}

///*** Beater interface methods ***///

// Config is a function to read config file
func (bt *Mysqlbeat) Config(b *beat.Beat) error {

	// Load beater beatConfig
	err := cfgfile.Read(&bt.beatConfig, "")
	if err != nil {
		return fmt.Errorf("Error reading config file: %v", err)
	}

	return nil
}

// Setup is a function to setup all beat config & info into the beat struct
func (bt *Mysqlbeat) Setup(b *beat.Beat) error {

	if len(bt.beatConfig.Mysqlbeat.Queries) < 1 {
		err := fmt.Errorf("there are no queries to execute")
		return err
	}

	if len(bt.beatConfig.Mysqlbeat.Queries) != len(bt.beatConfig.Mysqlbeat.QueryTypes) {
		err := fmt.Errorf("error on config file, queries array length != queryTypes array length (each query should have a corresponding type on the same index)")
		return err
	}

	// Setting defaults for missing config
	if bt.beatConfig.Mysqlbeat.Period == "" {
		logp.Info("Period not selected, proceeding with '%v' as default", defaultPeriod)
		bt.beatConfig.Mysqlbeat.Period = defaultPeriod
	}

	if bt.beatConfig.Mysqlbeat.Hostname == "" {
		logp.Info("Hostname not selected, proceeding with '%v' as default", defaultHostname)
		bt.beatConfig.Mysqlbeat.Hostname = defaultHostname
	}

	if bt.beatConfig.Mysqlbeat.Port == "" {
		logp.Info("Port not selected, proceeding with '%v' as default", defaultPort)
		bt.beatConfig.Mysqlbeat.Port = defaultPort
	}

	if bt.beatConfig.Mysqlbeat.Username == "" {
		logp.Info("Username not selected, proceeding with '%v' as default", defaultUsername)
		bt.beatConfig.Mysqlbeat.Username = defaultUsername
	}

	if bt.beatConfig.Mysqlbeat.Password == "" && bt.beatConfig.Mysqlbeat.EncryptedPassword == "" {
		logp.Info("Password not selected, proceeding with default password")
		bt.beatConfig.Mysqlbeat.Password = defaultPassword
	}

	if bt.beatConfig.Mysqlbeat.DeltaWildcard == "" {
		logp.Info("DeltaWildcard not selected, proceeding with '%v' as default", defaultDeltaWildcard)
		bt.beatConfig.Mysqlbeat.DeltaWildcard = defaultDeltaWildcard
	}

	if bt.beatConfig.Mysqlbeat.DeltaKeyWildcard == "" {
		logp.Info("DeltaKeyWildcard not selected, proceeding with '%v' as default", defaultDeltaKeyWildcard)
		bt.beatConfig.Mysqlbeat.DeltaKeyWildcard = defaultDeltaKeyWildcard
	}

	// Parse the Period string
	var durationParseError error
	bt.period, durationParseError = time.ParseDuration(bt.beatConfig.Mysqlbeat.Period)
	if durationParseError != nil {
		return durationParseError
	}

	// Handle password decryption and save in the bt
	if bt.beatConfig.Mysqlbeat.Password != "" {
		bt.password = bt.beatConfig.Mysqlbeat.Password
	} else if bt.beatConfig.Mysqlbeat.EncryptedPassword != "" {
		aesCipher, err := aes.NewCipher([]byte(secret))
		if err != nil {
			return err
		}
		cfbDecrypter := cipher.NewCFBDecrypter(aesCipher, commonIV)
		chiperText, err := hex.DecodeString(bt.beatConfig.Mysqlbeat.EncryptedPassword)
		if err != nil {
			return err
		}
		plaintextCopy := make([]byte, len(chiperText))
		cfbDecrypter.XORKeyStream(plaintextCopy, chiperText)
		bt.password = string(plaintextCopy)
	}

	// init the oldValues and oldValuesAge array
	bt.oldValues = common.MapStr{"mysqlbeat": "init"}
	bt.oldValuesAge = common.MapStr{"mysqlbeat": "init"}

	// Save config values to the bt
	bt.hostname = bt.beatConfig.Mysqlbeat.Hostname
	bt.port = bt.beatConfig.Mysqlbeat.Port
	bt.username = bt.beatConfig.Mysqlbeat.Username
	bt.queries = bt.beatConfig.Mysqlbeat.Queries
	bt.queryTypes = bt.beatConfig.Mysqlbeat.QueryTypes
	bt.deltaWildcard = bt.beatConfig.Mysqlbeat.DeltaWildcard
	bt.deltaKeyWildcard = bt.beatConfig.Mysqlbeat.DeltaKeyWildcard

	safeQueries := true

	logp.Info("Total # of queries to execute: %d", len(bt.queries))
	for index, queryStr := range bt.beatConfig.Mysqlbeat.Queries {

		strCleanQuery := strings.TrimSpace(strings.ToUpper(queryStr))

		if !strings.HasPrefix(strCleanQuery, "SELECT") && !strings.HasPrefix(strCleanQuery, "SHOW") || strings.ContainsAny(strCleanQuery, ";") {
			safeQueries = false
		}

		logp.Info("Query #%d (type: %s): %s", index+1, bt.queryTypes[index], queryStr)
	}

	if !safeQueries {
		err := fmt.Errorf("Only SELECT/SHOW queries are allowed (the char ; is forbidden)")
		return err
	}

	return nil
}

// Run is a functions that runs the beat
func (bt *Mysqlbeat) Run(b *beat.Beat) error {
	logp.Info("mysqlbeat is running! Hit CTRL-C to stop it.")

	file, err := os.OpenFile(resumeMultipleRowsFile, os.O_CREATE, 0666)
	if err != nil {
		logp.Err("cannot open file %s", resumeMultipleRowsFile)
	}
	file.Close()

	ticker := time.NewTicker(bt.period)

	go bt.listenResumeFile()

	for {
		select {
		case <-bt.done:
			return nil
		case <-ticker.C:
			err := bt.beat(b)
			if err != nil {
				return err
			}
		}
	}
}

// Cleanup is a function that does nothing on this beat :)
func (bt *Mysqlbeat) Cleanup(b *beat.Beat) error {
	return nil
}

// Stop is a function that runs once the beat is stopped
func (bt *Mysqlbeat) Stop() {
	close(bt.done)
}

///*** mysqlbeat methods ***///

// beat is a function that iterate over the query array, generate and publish events
func (bt *Mysqlbeat) beat(b *beat.Beat) error {

	// Build the MySQL connection string
	connString := fmt.Sprintf("%v:%v@tcp(%v:%v)/", bt.username, bt.password, bt.hostname, bt.port)

	db, err := sql.Open("mysql", connString)
	if err != nil {
		return err
	}
	defer db.Close()

	// Create a two-columns event for later use
	var twoColumnEvent common.MapStr

LoopQueries:
	for index, queryStr := range bt.queries {
		var lastResumeEvent common.MapStr
		var uniKey string
		var column string

		// Log the query run time and run the query
		queryStr, uniKey, column = bt.query(index, queryStr)
		dtNow := time.Now()
		rows, err := db.Query(queryStr)
		if err != nil {
			return err
		}

		// Populate columns array
		columns, err := rows.Columns()
		if err != nil {
			return err
		}

		// Populate the two-columns event
		if bt.queryTypes[index] == queryTypeTwoColumns {
			twoColumnEvent = common.MapStr{
				"@timestamp": common.Time(dtNow),
				"type":       queryTypeTwoColumns,
			}
		}

	LoopRows:
		for rows.Next() {

			switch bt.queryTypes[index] {
			case queryTypeSingleRow, queryTypeSlaveDelay:
				// Generate an event from the current row
				event, err := bt.generateEventFromRow(rows, columns, bt.queryTypes[index], dtNow)

				if err != nil {
					logp.Err("Query #%v error generating event from rows: %v", index+1, err)
				} else if event != nil {
					b.Events.PublishEvent(event)
					logp.Info("%v event sent", bt.queryTypes[index])
				}
				// breaking after the first row
				break LoopRows

			case queryTypeMultipleRows:
				// Generate an event from the current row
				event, err := bt.generateEventFromRow(rows, columns, bt.queryTypes[index], dtNow)

				if err != nil {
					logp.Err("Query #%v error generating event from rows: %v", index+1, err)
					break LoopRows
				} else if event != nil {
					b.Events.PublishEvent(event)
					logp.Info("%v event sent", bt.queryTypes[index])
				}

				// Move to the next row
				continue LoopRows

			case queryTypeResumeMultipleRows:
				// Generate an event from the current row
				event, err := bt.generateEventFromRow(rows, columns, bt.queryTypes[index], dtNow)

				if err != nil {
					logp.Err("Query #%v error generating event from rows: %v", index+1, err)
					break LoopRows
				} else if event != nil {
					b.Events.PublishEvent(event)
					logp.Info("%v event sent", bt.queryTypes[index])
				}

				lastResumeEvent = event

				// Move to the next row
				continue LoopRows

			case queryTypeTwoColumns:
				// append current row to the two-columns event
				err := bt.appendRowToEvent(twoColumnEvent, rows, columns, dtNow)

				if err != nil {
					logp.Err("Query #%v error appending two-columns event: %v", index+1, err)
					break LoopRows
				}

				// Move to the next row
				continue LoopRows
			}
		}

		if lastResumeEvent != nil && uniKey != "" && column != "" {
			resumeValue := fmt.Sprintf("%s|%s", uniKey, fmt.Sprintf("%v", lastResumeEvent[column]))
			bt.resumeFile <- resumeValue
		}

		// If the two-columns event has data, publish it
		if bt.queryTypes[index] == queryTypeTwoColumns && len(twoColumnEvent) > 2 {
			b.Events.PublishEvent(twoColumnEvent)
			logp.Info("%v event sent", queryTypeTwoColumns)
			twoColumnEvent = nil
		}

		rows.Close()
		if err = rows.Err(); err != nil {
			logp.Err("Query #%v error closing rows: %v", index+1, err)
			continue LoopQueries
		}
	}

	// Great success!
	return nil
}

// appendRowToEvent appends the two-column event the current row data
func (bt *Mysqlbeat) appendRowToEvent(event common.MapStr, row *sql.Rows, columns []string, rowAge time.Time) error {

	// Make a slice for the values
	values := make([]sql.RawBytes, len(columns))

	// Copy the references into such a []interface{} for row.Scan
	scanArgs := make([]interface{}, len(values))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	// Get RawBytes from data
	err := row.Scan(scanArgs...)
	if err != nil {
		return err
	}

	// First column is the name, second is the value
	strColName := string(values[0])
	strColValue := string(values[1])
	strColType := columnTypeString
	strEventColName := strings.Replace(strColName, bt.deltaWildcard, "_PERSECOND", 1)

	// Try to parse the value to an int64
	nColValue, err := strconv.ParseInt(strColValue, 0, 64)
	if err == nil {
		strColType = columnTypeInt
	}

	// Try to parse the value to a float64
	fColValue, err := strconv.ParseFloat(strColValue, 64)
	if err == nil {
		// If it's not already an established int64, set type to float
		if strColType == columnTypeString {
			strColType = columnTypeFloat
		}
	}

	// If the column name ends with the deltaWildcard
	if strings.HasSuffix(strColName, bt.deltaWildcard) {
		var exists bool
		_, exists = bt.oldValues[strColName]

		// If an older value doesn't exist
		if !exists {
			// Save the current value in the oldValues array
			bt.oldValuesAge[strColName] = rowAge

			if strColType == columnTypeString {
				bt.oldValues[strColName] = strColValue
			} else if strColType == columnTypeInt {
				bt.oldValues[strColName] = nColValue
			} else if strColType == columnTypeFloat {
				bt.oldValues[strColName] = fColValue
			}
		} else {
			// If found the old value's age
			if dtOldAge, ok := bt.oldValuesAge[strColName].(time.Time); ok {
				delta := rowAge.Sub(dtOldAge)

				if strColType == columnTypeInt {
					var calcVal int64

					// Get old value
					oldVal, _ := bt.oldValues[strColName].(int64)
					if nColValue > oldVal {
						// Calculate the delta
						devResult := float64((nColValue - oldVal)) / float64(delta.Seconds())
						// Round the calculated result back to an int64
						calcVal = roundF2I(devResult, .5)
					} else {
						calcVal = 0
					}

					// Add the delta value to the event
					event[strEventColName] = calcVal

					// Save current values as old values
					bt.oldValues[strColName] = nColValue
					bt.oldValuesAge[strColName] = rowAge
				} else if strColType == columnTypeFloat {
					var calcVal float64

					// Get old value
					oldVal, _ := bt.oldValues[strColName].(float64)
					if fColValue > oldVal {
						// Calculate the delta
						calcVal = (fColValue - oldVal) / float64(delta.Seconds())
					} else {
						calcVal = 0
					}

					// Add the delta value to the event
					event[strEventColName] = calcVal

					// Save current values as old values
					bt.oldValues[strColName] = fColValue
					bt.oldValuesAge[strColName] = rowAge
				} else {
					event[strEventColName] = strColValue
				}
			}
		}
	} else {
		// Not a delta column, add the value to the event as is
		if strColType == columnTypeString {
			event[strEventColName] = strColValue
		} else if strColType == columnTypeInt {
			event[strEventColName] = nColValue
		} else if strColType == columnTypeFloat {
			event[strEventColName] = fColValue
		}
	}

	// Great success!
	return nil
}

func (bt *Mysqlbeat) readResumeIndex(index string, reCh chan string) {
	file, err := os.OpenFile(resumeMultipleRowsFile, os.O_RDONLY, 0)
	if err != nil {
		reCh <- ""
		return
	}
	reader := bufio.NewReader(file)
	for {
		inputString, readerError := reader.ReadString('\n')
		if readerError != nil {
			reCh <- ""
			return
		}
		var m ResumeIndex
		err := json.Unmarshal([]byte(inputString), &m)
		if err != nil {
			reCh <- ""
			return
		}
		if m.Index == index {
			reCh <- m.Value
			return
		}
	}
}

func (bt *Mysqlbeat) query(index int, queryStr string) (string, string, string) {
	if bt.queryTypes[index] != queryTypeResumeMultipleRows {
		return queryStr, "", ""
	}
	reCh := make(chan string)

	re := regexp.MustCompile(`\{\w*\|\w*\|\w*\}`)
	target := re.FindString(queryStr)
	reOne := regexp.MustCompile(`\w*\|\w*\|\w*`)
	oneString := reOne.FindString(target)
	values := strings.Split(oneString, "|")

	go bt.readResumeIndex(values[0], reCh)
	replace := <-reCh

	if replace == "" {
		return re.ReplaceAllString(queryStr, values[1]), values[0], values[2]
	} else {
		return re.ReplaceAllString(queryStr, replace), values[0], values[2]
	}
}

func (bt *Mysqlbeat) listenResumeFile() {
	for {
		resumeValue := <-bt.resumeFile
		values := strings.Split(resumeValue, "|")
		resumeIndex := &ResumeIndex{Index: values[0], Value: values[1]}
		go bt.writeToResumeFile(resumeIndex)
	}
}

func (bt *Mysqlbeat) writeToResumeFile(resumeIndex *ResumeIndex) {
	bt.mu.Lock()
	contents := []string{}
	file, err := os.OpenFile(resumeMultipleRowsFile, os.O_CREATE|os.O_RDONLY, 0)
	if err != nil {
		logp.Err("while write to resumeFile open file err", err)

	}
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			contents = append(contents, line)
		}
		if readErr != nil {
			break
		}
	}
	file.Close()

	file, err = os.OpenFile(resumeMultipleRowsFile, os.O_WRONLY|os.O_TRUNC|os.O_SYNC, 0666)
	defer file.Close()
	if err != nil {
		logp.Err("while write to resumeFile open file err", err)
		bt.mu.Unlock()
	}
	writer := bufio.NewWriter(file)

	find := false
	for i := 0; i < len(contents); i++ {
		var m ResumeIndex
		jsErr := json.Unmarshal([]byte(contents[i]), &m)
		if jsErr != nil {
			continue
		}
		if m.Index == resumeIndex.Index {
			find = true
			m.Value = resumeIndex.Value
		}
		jsContent, jsMErr := json.Marshal(m)
		if jsMErr != nil {
			continue
		}
		contents[i] = string(jsContent)
	}
	if !find {
		findContent, _ := json.Marshal(resumeIndex)
		contents = append(contents, string(findContent))
	}
	for content := range contents {
		writer.WriteString(fmt.Sprintf("%s\n", contents[content]))
	}
	writer.Flush()
	bt.mu.Unlock()
}

// generateEventFromRow creates a new event from the row data and returns it
func (bt *Mysqlbeat) generateEventFromRow(row *sql.Rows, columns []string, queryType string, rowAge time.Time) (common.MapStr, error) {

	// Make a slice for the values
	values := make([]sql.RawBytes, len(columns))

	// Copy the references into such a []interface{} for row.Scan
	scanArgs := make([]interface{}, len(values))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	// Create the event and populate it
	event := common.MapStr{
		"@timestamp": common.Time(rowAge),
		"type":       queryType,
	}

	// Get RawBytes from data
	err := row.Scan(scanArgs...)
	if err != nil {
		return nil, err
	}

	// Loop on all columns
	for i, col := range values {
		// Get column name and string value
		strColName := string(columns[i])
		strColValue := string(col)
		strColType := columnTypeString

		// Skip column proccessing when query type is show-slave-delay and the column isn't Seconds_Behind_Master
		if queryType == queryTypeSlaveDelay && strColName != columnNameSlaveDelay {
			continue
		}

		// Set the event column name to the original column name (as default)
		strEventColName := strColName

		// Remove unneeded suffix, add _PERSECOND to calculated columns
		if strings.HasSuffix(strColName, bt.deltaKeyWildcard) {
			strEventColName = strings.Replace(strColName, bt.deltaKeyWildcard, "", 1)
		} else if strings.HasSuffix(strColName, bt.deltaWildcard) {
			strEventColName = strings.Replace(strColName, bt.deltaWildcard, "_PERSECOND", 1)
		}

		// Try to parse the value to an int64
		nColValue, err := strconv.ParseInt(strColValue, 0, 64)
		if err == nil {
			strColType = columnTypeInt
		}

		// Try to parse the value to a float64
		fColValue, err := strconv.ParseFloat(strColValue, 64)
		if err == nil {
			// If it's not already an established int64, set type to float
			if strColType == columnTypeString {
				strColType = columnTypeFloat
			}
		}

		// If the column name ends with the deltaWildcard
		if (queryType == queryTypeSingleRow || queryType == queryTypeMultipleRows) && strings.HasSuffix(strColName, bt.deltaWildcard) {

			var strKey string

			// Get unique row key, if it's a single row - use the column name
			if queryType == queryTypeSingleRow {
				strKey = strColName
			} else if queryType == queryTypeMultipleRows {

				// If the query has multiple rows, a unique row key must be defind using the delta key wildcard and the column name
				strKey, err = getKeyFromRow(bt, values, columns)
				if err != nil {
					return nil, err
				}

				strKey += strColName
			}

			var exists bool
			_, exists = bt.oldValues[strKey]

			// If an older value doesn't exist
			if !exists {
				// Save the current value in the oldValues array
				bt.oldValuesAge[strKey] = rowAge

				if strColType == columnTypeString {
					bt.oldValues[strKey] = strColValue
				} else if strColType == columnTypeInt {
					bt.oldValues[strKey] = nColValue
				} else if strColType == columnTypeFloat {
					bt.oldValues[strKey] = fColValue
				}
			} else {
				// If found the old value's age
				if dtOldAge, ok := bt.oldValuesAge[strKey].(time.Time); ok {
					delta := rowAge.Sub(dtOldAge)

					if strColType == columnTypeInt {
						var calcVal int64

						// Get old value
						oldVal, _ := bt.oldValues[strKey].(int64)

						if nColValue > oldVal {
							// Calculate the delta
							devResult := float64((nColValue - oldVal)) / float64(delta.Seconds())
							// Round the calculated result back to an int64
							calcVal = roundF2I(devResult, .5)
						} else {
							calcVal = 0
						}

						// Add the delta value to the event
						event[strEventColName] = calcVal

						// Save current values as old values
						bt.oldValues[strKey] = nColValue
						bt.oldValuesAge[strKey] = rowAge
					} else if strColType == columnTypeFloat {
						var calcVal float64
						oldVal, _ := bt.oldValues[strKey].(float64)

						if fColValue > oldVal {
							// Calculate the delta
							calcVal = (fColValue - oldVal) / float64(delta.Seconds())
						} else {
							calcVal = 0
						}

						// Add the delta value to the event
						event[strEventColName] = calcVal

						// Save current values as old values
						bt.oldValues[strKey] = fColValue
						bt.oldValuesAge[strKey] = rowAge
					} else {
						event[strEventColName] = strColValue
					}
				}
			}
		} else {
			// Not a delta column, add the value to the event as is
			if strColType == columnTypeString {
				event[strEventColName] = strColValue
			} else if strColType == columnTypeInt {
				event[strEventColName] = nColValue
			} else if strColType == columnTypeFloat {
				event[strEventColName] = fColValue
			}
		}
	}

	// If the event has no data, set to nil
	if len(event) == 2 {
		event = nil
	}

	return event, nil
}

// getKeyFromRow is a function that returns a unique key from row
func getKeyFromRow(bt *Mysqlbeat, values []sql.RawBytes, columns []string) (strKey string, err error) {

	keyFound := false

	// Loop on all columns
	for i, col := range values {
		// Get column name and string value
		if strings.HasSuffix(string(columns[i]), bt.deltaKeyWildcard) {
			strKey += string(col)
			keyFound = true
		}
	}

	if !keyFound {
		err = fmt.Errorf("query type multiple-rows requires at least one delta key column")
	}

	return strKey, err
}

// roundF2I is a function that returns a rounded int64 from a float64
func roundF2I(val float64, roundOn float64) (newVal int64) {
	var round float64

	digit := val
	_, div := math.Modf(digit)
	if div >= roundOn {
		round = math.Ceil(digit)
	} else {
		round = math.Floor(digit)
	}

	return int64(round)
}
