package main

import (
	"fmt"
	"github.com/elastic/beats/libbeat/beat"
	"strings"

	"mysqlbeat/beater"
	"mysqlbeat/config"
)

func main() {
	mysqlConfig := config.MysqlbeatConfig{Period: "10s", Hostname: "127.0.0.1", Port: "3306", Username: "root",
		Password: "root", EncryptedPassword: "", Queries: []string{"select id,title,subtitle,status,type as c_type, maxStudentNum,price,originPrice,coinPrice,originCoinPrice,income,lessonNum,rating,ratingNum,categoryId,tags as c_tags,smallPicture,middlePicture,largePicture,about,teacherIds,recommended,recommendedSeq,studentNum,hitNum,userId,discount,deadlineNotify,useInClassroom,watchLimit,createdTime,noteNum,locked,buyable,'es.mysql.course' as type from test.course where updatedTime > {edusoho_course_updatedTime|0|updatedTime} order by updatedTime LIMIT 100"},
		QueryTypes: []string{"resume-multiple-rows"}, DeltaWildcard: "__DELTA", DeltaKeyWildcard: "__DELTAKEY"}
	config := &config.Config{Mysqlbeat: mysqlConfig}
	mysqlbeat := beater.New()
	mysqlbeat.MockSetConfig(config)
	for index, queryStr := range mysqlConfig.Queries {
		fmt.Println(index, queryStr)
		fmt.Println(strings.TrimSpace(strings.ToUpper(queryStr)))
	}
	beat := beat.NewBeat("mysqlbeat", "", mysqlbeat)
	mysqlbeat.Setup(beat)
	mysqlbeat.Run(beat)
}
