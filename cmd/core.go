package cmd

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"syscall"
	"math/big"
	"time"
	"unicode"
	mrand "math/rand"
	"strconv"

	"check-password-strength/assets"

	colorable "github.com/mattn/go-colorable"
	"github.com/nbutton23/zxcvbn-go"
	"github.com/olekukonko/tablewriter"
	"golang.org/x/crypto/ssh/terminal"
)

type csvHeader map[string]*[]string

type csvHeaderOrder map[string]int

type csvRow struct {
	URL      string
	Username string
	Password string
}

type jsonData struct {
	Words []string `json:"words"`
}

type statistics struct {
	TotCount       int
	WordsCount     int
	ScoreCount     []int
	ScorePerc      []int
	DuplicateCount int
}

type duplicates map[string][]int

func loadBundleDict() ([]string, error) {

	var assetDict []string

	for _, an := range assets.AssetNames() {

		var d jsonData

		data, err := assets.Asset(an)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(data, &d)
		if err != nil {
			return nil, err
		}

		assetDict = append(assetDict, d.Words...)
	}

	return assetDict, nil
}

func loadCustomDict(filename string) ([]string, error) {

	var customDict []string
	var d jsonData

	log.Debugf("custom dict filename: %s", filename)

	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(data, &d)
	if err != nil {
		return nil, err
	}

	if len(d.Words) == 0 {
		return nil, errors.New("Object 'words' is empty, custom dictionary not loaded")
	}

	customDict = append(customDict, d.Words...)

	return customDict, nil
}

func loadAllDict(filename string) ([]string, error) {
	// load bundle dictionaries
	assetDict, err := loadBundleDict()
	if err != nil {
		log.Debug("errore loading bundled dictionaries")
		return nil, err
	}

	// load custom dictionaries
	if filename != "" {
		customDict, err := loadCustomDict(filename)
		if err != nil {
			log.Debug("error loading custom dictionary")
			return nil, err
		}
		assetDict = append(assetDict, customDict...)
	}

	return assetDict, nil
}

func askUsernamePassword() (string, string, error) {

	var username string

	fmt.Print("Enter Username: ")
	fmt.Scanln(&username)
	fmt.Print("Enter Password: ")
	password, err := terminal.ReadPassword(int(syscall.Stdin))
	fmt.Println()

	if err != nil {
		return "", "", err
	}

	return username, string(password), nil
}

func checkMultiplePassword(csvfile, jsonfile string, interactive, stats bool, limit int, outFileName string) error {

	var output [][]string

	// load all dictionaries
	allDict, err := loadAllDict(jsonfile)
	if err != nil {
		return err
	}

	// initialize statistics
	stat := initStats(len(allDict))
	duplicate := duplicates{}

	// generate seed
	seed, err := generateSeed()
	if err != nil {
		return err
	}

	lines, order, err := readCsv(csvfile)
	if err != nil {
		return err
	}
	log.Debugf("order: %v\n", order)

	index := 0
	for _, line := range lines {
		data := csvRow{
			URL:      line[order["url"]],
			Username: line[order["username"]],
			Password: line[order["password"]],
		}

		passwordStength := zxcvbn.PasswordStrength(data.Password, append(allDict, data.Username))

		// filter output based on limit flag
		if limit >= passwordStength.Score {
			log.Debugf("Included: score: %d => filter: %d", passwordStength.Score, limit)

			// check if password is already used
			hash := generateHash(seed, data.Password)
			duplicate[hash] = append(duplicate[hash], index)
			index++

			// data.Password = redactPassword(data.Password)
			output = append(output, []string{data.URL, data.Username, data.Password,
				fmt.Sprintf("%d", passwordStength.Score),
				fmt.Sprintf("%.2f", passwordStength.Entropy),
				passwordStength.CrackTimeDisplay,
				"",
			})
			// update statistics
			stat.ScoreCount[passwordStength.Score] = stat.ScoreCount[passwordStength.Score] + 1
			stat.TotCount = stat.TotCount + 1
		}

	}

	// add hash to identify duplicated passwords
	log.Debugf("Start marking duplicates")
	for h, v := range duplicate {
		if len(v) > 1 {
			for _, i := range v {
				output[i][6] = h
				stat.DuplicateCount = stat.DuplicateCount + 1
			}
		}
	}
	log.Debugf("End marking duplicates")
	var newPasswords []string
	// show statistics report
	if stats {
		showStats(stat, colorable.NewColorableStdout())
	} else {
		newPasswords = showTable(output, colorable.NewColorableStdout(), allDict)
	}

	

	// Process passwords with strength less than 3
	if outFileName != ""{
		var createNewFile bool
		var userInputAnswers []string
		for _, passwordData := range output {
			passwordStrength, err := strconv.Atoi(passwordData[3]) // Assuming password strength is at index 3
			if err != nil {
				return err
			}

			if passwordStrength < 3 {
				// Ask the user if the password should be replaced
				replacePassword := false
				if csvfile != "" {
					fmt.Printf("The password for user: %s has low strength (score: %d). Would you like to replace it with the suggested password? (y/n): ",
						passwordData[1], passwordStrength)
					var userInput string
					_, err := fmt.Scanln(&userInput)
					if err != nil {
						return err
					}
					replacePassword = strings.ToLower(strings.TrimSpace(userInput)) == "y"
					if replacePassword == true {
						createNewFile = true
					}
					userInputAnswers = append(userInputAnswers, strings.ToLower(userInput))
				}
			} else{
				userInputAnswers = append(userInputAnswers, "n")
			}
		}
		// Replace the password if the user agreed
		if createNewFile {

			// Write the updated data back to the CSV file
			err = changePassword(csvfile, outFileName, newPasswords, userInputAnswers)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func addRandChar(currentPassword string)(string, error){
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()-_=+[]{}|;:'\",.<>/?`~"
	password := []byte(currentPassword)
	charIndex := mrand.Intn(len(charset))
	password = append(password, charset[charIndex])

	return string(password), nil
}

func generateRandomPassword(minLength, maxLength int) (string, error) {
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()-_=+[]{}|;:'\",.<>/?`~"
	
	passwordLength, err := rand.Int(rand.Reader, big.NewInt(int64(maxLength-minLength+1)))
		if err != nil {
		return "", err
	}
	
	length := int(passwordLength.Int64()) + minLength
	password := make([]byte, length)
	
	for i := range password {
		charIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		password[i] = charset[charIndex.Int64()]
	}
	
	return string(password), nil
}

func generateStrongerPassword(currentPassword string, minLength, maxLength int) (string, error) {
	// charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()-_=+[]{}|;:'\",.<>/?`~"
	mrand.Seed(time.Now().UnixNano())
	// Start with the current password as a seed
	password := []byte(currentPassword)

	// Define a function to randomly capitalize a letter
	capitalize := func(char byte) byte {
		if 'a' <= char && char <= 'z' {
			return char - 'a' + 'A'
		}
		return char
	}

	for i := range password {
		// Randomly capitalize a letter
		randNum1 := mrand.Intn(3)

		if randNum1 == 0 {
			password[i] = capitalize(password[i])
		} else if randNum1 == 1 {
			mrand.Seed(time.Now().UnixNano())
			randNum2 := mrand.Intn(2)
			switch unicode.ToLower(rune(password[i])) {
			case 'a':
				// Randomly choose between '4' and '@'
				if randNum2 == 0 {
					password[i] = '4'
				} else {
					password[i] = '@'
				}
			case 'i':
				if randNum2 == 0 {
					password[i] = '1'
				} else {
					password[i] = '!'
				}
			case 'l':
				if randNum2 == 0 {
					password[i] = '|'
				} else {
					password[i] = '7'
				}
			case 'c':
				if randNum2 == 0 {
					password[i] = '('
				} else {
					password[i] = '['
				}
			case 's':
				password[i] = '$'
			}
		}
	}

	// Add up to 4 characters to the end
	// for i := 0; i < 4; i++ {
	// 	charIndex := mrand.Intn(len(charset))
	// 	password = append(password, charset[charIndex])
	// }
	
	return string(password), nil
}

func checkSinglePassword(username, password, jsonfile string, quiet, stats bool) error {

	var output [][]string

	// load all dictionaries
	allDict, err := loadAllDict(jsonfile)
	if err != nil {
		return err
	}

	// initialize statistics
	stat := initStats(len(allDict))

	passwordStength := zxcvbn.PasswordStrength(password, append(allDict, username))
	// password = redactPassword(password)

	// update statistics
	stat.ScoreCount[passwordStength.Score] = stat.ScoreCount[passwordStength.Score] + 1
	stat.TotCount = stat.TotCount + 1

	if quiet {
		os.Exit(passwordStength.Score)
	}

	output = append(output, []string{"", username, password,
		fmt.Sprintf("%d", passwordStength.Score),
		fmt.Sprintf("%.2f", passwordStength.Entropy),
		passwordStength.CrackTimeDisplay,
		"",
	})

	if stats {
		showStats(stat, colorable.NewColorableStdout())
	} else {
		newPasswords := showTable(output, colorable.NewColorableStdout(), allDict)
		if newPasswords[0] != ""{
			return nil
		}
	}

	return nil
}

func changePassword(filename string, outFileName string, newPasswords []string, userInputAnswers []string) error {
    fIn, err := os.Open(filename)
    must(err)
    defer fIn.Close()
    fOut, err := os.Create(outFileName)
    must(err)
    defer fOut.Close()

    r := csv.NewReader(fIn)
    w := csv.NewWriter(fOut)

    for i := 0; i < len(newPasswords); i++ {
        record, err := r.Read()
        if err == io.EOF {
            break
        }
		if i != 0{
			must(err)

			if userInputAnswers[i - 1] == "y" {
				record[2] = newPasswords[i - 1]
			}

		}
		w.Write(record)
    }

    w.Flush()
    must(w.Error())

	return nil
}

func must(err error) {
    if err != nil {
        log.Fatal(err)
    }
}

func readCsv(filename string) ([][]string, csvHeaderOrder, error) {

	log.Debugf("csv filename: %s", filename)

	// Open CSV file
	f, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	lines, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, nil, err
	}

	if len(lines) == 0 {
		return nil, nil, errors.New("File empty")
	}
	header := lines[0]

	order, err := checkCSVHeader(header)
	if err != nil {
		return nil, nil, err
	}

	// remove note records
	lines = removeNotes(lines)

	// remove header
	return lines[1:], order, nil
}

func checkCSVHeader(header []string) (csvHeaderOrder, error) {

	// initialize structs
	headers := &csvHeader{
		"url":      &[]string{"url", "login_uri", "web site"},
		"username": &[]string{"username", "login_username", "login name"},
		"password": &[]string{"password", "login_password"},
	}

	log.Debugf("header: %v", header)

	order := make(csvHeaderOrder)

	for position, fieldFromFile := range header {
		// check header
		for k, h := range *headers {
			for _, v := range *h {
				if strings.ToLower(fieldFromFile) == v {
					if _, ok := order[k]; ok {
						return nil, errors.New("Header not valid")
					}
					order[k] = position
				}
			}
		}
	}

	if len(order) != 3 {
		return nil, errors.New("Header not valid")
	}
	return order, nil
}

func removeNotes(lines [][]string) [][]string {
	// remove Bitwarden notes (field 2: type = "note")
	var nonotes [][]string
	for _, line := range lines {
		if line[2] != "note" {
			nonotes = append(nonotes, line)
		}
	}
	return nonotes
}

func redactPassword(p string) string {
	if len(p) < 3 {
		return "********"
	}
	return fmt.Sprintf("%s******%s", p[0:1], p[len(p)-1:])
}

func truncateURL(url string) string {
	if len(url) > 25 && strings.HasPrefix(strings.ToLower(url), "https://") {
		url = fmt.Sprintf("%s...", url[8:])
	}
	if len(url) > 25 && strings.HasPrefix(strings.ToLower(url), "http://") {
		url = fmt.Sprintf("%s...", url[7:])
	}
	if len(url) > 25 {
		return fmt.Sprintf("%s...", url[0:22])
	}
	return url
}

func generateSeed() ([]byte, error) {
	buf := make([]byte, 16)
	_, err := rand.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func generateHash(seed []byte, password string) string {
	sha1 := sha512.Sum512(append(seed, []byte(password)...))
	return fmt.Sprintf("%x", sha1)[:8]
}

func initStats(c int) statistics {
	return statistics{
		TotCount:       0,
		WordsCount:     c,
		ScoreCount:     []int{0, 0, 0, 0, 0},
		ScorePerc:      []int{0, 0, 0, 0, 0},
		DuplicateCount: 0,
	}
}

func showTable(data [][]string, w io.Writer, allDict []string) []string {
	// writer is a s parameter to pass buffer during tests
	table := tablewriter.NewWriter(w)
	table.SetHeader([]string{ "Username", "Password", "Score (0-4)", "Time to crack", "Random", "Suggested"})
	table.SetBorder(false)
	table.SetAlignment(tablewriter.ALIGN_LEFT)

	var newPasswords []string

	for _, row := range data {
		var score string
		var scoreColor int

		// url := truncateURL(row[0])
		var generatedPassword string
		var err error
		var strongerPassword string
		var err2 error
		switch row[3] {
		case "0":
			score = " 0 - Really bad "
			scoreColor = tablewriter.BgRedColor
			generatedPassword, err = generateRandomPassword(10, 20)
			strongerPassword, err2 = generateStrongerPassword(row[2], 10, 20)
			for zxcvbn.PasswordStrength(strongerPassword, append(allDict, row[1])).Score < 3 {
				strongerPassword, _ = addRandChar(strongerPassword)
			}
			newPasswords = append(newPasswords, strongerPassword)
		case "1":
			score = " 1 - Bad        "
			scoreColor = tablewriter.BgHiRedColor
			generatedPassword, err = generateRandomPassword(10, 20)
			strongerPassword, err2 = generateStrongerPassword(row[2], 10, 20)
			for zxcvbn.PasswordStrength(strongerPassword, append(allDict, row[1])).Score < 3 {
				strongerPassword, _ = addRandChar(strongerPassword)
			}
			newPasswords = append(newPasswords, strongerPassword)
		case "2":
			score = " 2 - Weak       "
			scoreColor = tablewriter.BgHiYellowColor
			generatedPassword, err = generateRandomPassword(10, 20)
			strongerPassword, err2 = generateStrongerPassword(row[2], 10, 20)
			for zxcvbn.PasswordStrength(strongerPassword, append(allDict, row[1])).Score < 3 {
				strongerPassword, _ = addRandChar(strongerPassword)
			}
			newPasswords = append(newPasswords, strongerPassword)
		case "3":
			score = " 3 - Good       "
			scoreColor = tablewriter.BgHiGreenColor
			newPasswords = append(newPasswords, "")
		case "4":
			score = " 4 - Strong     "
			scoreColor = tablewriter.BgGreenColor
			newPasswords = append(newPasswords, "")
		}

		if err == nil && generatedPassword != "" && err2 == nil {
			colorRow := []string{ row[1], row[2], score, row[5], generatedPassword, strongerPassword}
			table.Rich(colorRow, []tablewriter.Colors{nil, nil, {scoreColor}})
		} else {
			colorRow := []string{ row[1], row[2], score, row[5], "", ""}
			table.Rich(colorRow, []tablewriter.Colors{nil, nil, {scoreColor}})
		}
		
	}

	table.Render()
	return newPasswords
}

func showStats(stat statistics, w io.Writer) {
	// writer is a s parameter to pass buffer during tests
	table := tablewriter.NewWriter(w)
	table.SetHeader([]string{"Description", "Count", "%", "Bar"})
	table.SetBorder(false)
	table.SetAutoWrapText(false)

	stat.ScorePerc = roundPercentage(stat.ScoreCount, stat.TotCount)

	data := [][]string{
		{"Password checked", fmt.Sprintf("%d", stat.TotCount), "", ""},
		{"Words in dictionaries", fmt.Sprintf("%d", stat.WordsCount), "", ""},
		{"Duplicated passwords", fmt.Sprintf("%d", stat.DuplicateCount), "", ""},
		{"Really bad passwords", fmt.Sprintf("%d", stat.ScoreCount[0]), fmt.Sprintf("%3d%%", stat.ScorePerc[0]), showBarPerc(stat.ScorePerc[0])},
		{"Bad passwords", fmt.Sprintf("%d", stat.ScoreCount[1]), fmt.Sprintf("%3d%%", stat.ScorePerc[1]), showBarPerc(stat.ScorePerc[1])},
		{"Weak passwords", fmt.Sprintf("%d", stat.ScoreCount[2]), fmt.Sprintf("%3d%%", stat.ScorePerc[2]), showBarPerc(stat.ScorePerc[2])},
		{"Good passwords", fmt.Sprintf("%d", stat.ScoreCount[3]), fmt.Sprintf("%3d%%", stat.ScorePerc[3]), showBarPerc(stat.ScorePerc[3])},
		{"Strong passwords", fmt.Sprintf("%d", stat.ScoreCount[4]), fmt.Sprintf("%3d%%", stat.ScorePerc[4]), showBarPerc(stat.ScorePerc[4])},
	}

	var scoreColor int
	for i, row := range data {
		switch i {
		case 3:
			scoreColor = tablewriter.BgRedColor
		case 4:
			scoreColor = tablewriter.BgHiRedColor
		case 5:
			scoreColor = tablewriter.BgHiYellowColor
		case 6:
			scoreColor = tablewriter.BgHiGreenColor
		case 7:
			scoreColor = tablewriter.BgGreenColor
		}
		// remove color is bar is 0%
		if row[1] == "0" {
			scoreColor = 0
		}
		table.Rich(row, []tablewriter.Colors{nil, nil, nil, {scoreColor}})
	}
	table.Render()
}

func showBarPerc(perc int) string {
	return fmt.Sprintf("%v", strings.Repeat(" ", perc))
}

func roundPercentage(scoreCount []int, totCount int) []int {

	type Percentage struct {
		Value float32
		Order int
	}

	roundedPerc := []int{}
	dataset := []Percentage{}
	totalPerc := 0

	//percentages []float32
	for i, score := range scoreCount {
		perc := float32(score) / float32(totCount) * 100.0
		dataset = append(dataset, Percentage{Value: perc, Order: i})
		totalPerc += int(perc)
	}
	diffToAdd := 100 - totalPerc

	// order by decimal
	sort.Slice(dataset, func(i, j int) bool {
		return dataset[i].Value-float32(int(dataset[i].Value)) > dataset[j].Value-float32(int(dataset[j].Value))
	})
	// distribute diff to get to 100%
	for n := 0; n < diffToAdd; n++ {
		dataset[n].Value++
	}
	// order by original position
	sort.Slice(dataset, func(i, j int) bool {
		return float32(dataset[i].Order) < float32(dataset[j].Order)
	})

	for _, n := range dataset {
		roundedPerc = append(roundedPerc, int(n.Value))
	}

	return roundedPerc
}

func getPwdStdin() (string, error) {

	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}

	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("Pipe error on stdin")
	}

	stdinBytes, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}

	// remove spaces and new line
	output := strings.TrimSpace(string(stdinBytes))

	return output, nil
}
