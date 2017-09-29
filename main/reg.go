package main

import (
	//"regexp"
	//"fmt"
	//"strings"
)
import (
	"strings"
	"fmt"
)

func main() {
	//regStr := "SELECT * FROM api.bill WHERE createdTime >= ${api_bill|0|createdTime}"
	//re, _ := regexp.Compile(`\$\{\w*\|\w*\|\w*\}`)
	//one := re.FindString(regStr)
	//fmt.Println(string(one))
	//
	//reOne, _ := regexp.Compile(`\w*\|\w*\|\w*`)
	//oneString := reOne.FindString(regStr)
	//fmt.Println(oneString)
	//values := strings.Split(oneString, "|")
	//fmt.Println(values)
	//
	//fmt.Println(re.ReplaceAllString(regStr, values[1]))

	resumeValue := "updatedTime|0"
	values := strings.Split(resumeValue, "|")
	fmt.Println(values)
}