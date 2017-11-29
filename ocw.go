// ocw
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	oapi "github.com/Nemo08/goosmapi"
	_ "github.com/davecgh/go-spew/spew"
	"gopkg.in/telegram-bot-api.v4"
)

const (
	getdataworkers    = 5
	memMonitorTimeout = 60
	configpath        = "./config.json"
	messageString     = `<b>#%v</b>, редактор: <a href="http://hdyc.neis-one.org/?%s">%s</a>
	%s
	<code>%s</code>`
	telegramkey            = "286770490:AAHRcwOrEB-vgXeLTgJCSQ6rU6FYH5vK4ic"
	OSMAPIEndpoint         = "http://api.openstreetmap.org/api/0.6/changesets?%s"
	ChangesetRequestString = "bbox=%f,%f,%f,%f&time=%s"
	ocw_version            = "0.3"
)

type OCW struct {
	toSendOSMDataChannel chan OsmResponse    //канал сообщений: отправляем телеграмом
	toGetOSMDataChannel  chan int            //канал заданий: какую область скачать по id
	toWriteConfigPart    chan WriteToConfig  //канал конфигураций: изменения в конфигурации, последний чейнжсет
	toReadConfigSignal   chan bool           // канал запросов: для сигналов доправить конфигурацию в toReadConfig
	toReadConfig         chan JsonAreaConfig // канал получения полной конфигурации

	httpclient *http.Client
	areas      JsonAreaConfig
}

func (o *OCW) Start() {
	o.toSendOSMDataChannel = make(chan OsmResponse, 50)
	o.toGetOSMDataChannel = make(chan int, 50)
	o.toWriteConfigPart = make(chan WriteToConfig)
	o.toReadConfigSignal = make(chan bool)
	o.toReadConfig = make(chan JsonAreaConfig)
	o.httpclient = &http.Client{
		Timeout: time.Duration(time.Second * 5),
	}

	o.readConfig()

	go o.memMonitor()
	go o.telegramMessageSender()
	go o.telegrammAdminPanel()

	o.StateMonitor()

	var input string
	fmt.Scanln(&input)
}

func (o *OCW) StateMonitor() {
	for k, v := range o.areas.Areas {
		//запускаем таймеры для областей
		go o.timeWaiter(k, v.UpdateTime)
		//запрашиваем области первый раз без таймера
		go o.getChangesetList(o.httpclient, k, o.areas.Areas[k])
	}

	var (
		id   int
		conf WriteToConfig
	)

	go func() {
		for {
			select {
			case id = <-o.toGetOSMDataChannel:
				go o.getChangesetList(o.httpclient, id, o.areas.Areas[id])
				break
			case conf = <-o.toWriteConfigPart:
				o.areas.Areas[conf.id].LatestChangeset = conf.LatestChangeset
				o.writeConfig()
				break
			case <-o.toReadConfigSignal:
				o.toReadConfig <- o.areas
			}
		}
	}()
}

func (o *OCW) timeWaiter(id int, timeout int) {
	t := time.NewTicker(time.Duration(timeout) * time.Minute)
	for range t.C {
		o.toGetOSMDataChannel <- id
	}
}

func (o *OCW) getChangesetList(client *http.Client, areaid int, ar Area) {
	log.Printf("Запрашиваем у osm.org изменения в '%s'", ar.Comment)
	var (
		chId, maxchangeset int64
		changesets         []oapi.ChangesetInfo
	)

	changesets = o.getDWbody(ar, client)

	if len(changesets) > 0 {
		for k, _ := range changesets {
			chId = changesets[len(changesets)-k-1].ChangesetId
			if chId > ar.LatestChangeset {
				log.Println("Пишем изменения по '", ar.Comment, "' в канал ", ar.TelegrammChannel)

				o.toSendOSMDataChannel <- OsmResponse{
					Channel: ar.TelegrammChannel,
					Comment: ar.Comment,
					Bbox:    ar.Bbox,
					Resp:    changesets[len(changesets)-k-1]}

				if maxchangeset < chId {
					maxchangeset = chId
				}
			}
		}

		if maxchangeset > ar.LatestChangeset {
			o.toWriteConfigPart <- WriteToConfig{id: areaid, LatestChangeset: maxchangeset}
		} else {
			log.Printf("Изменений по '%s' за %v мин нет, последний чейнджсет %d", ar.Comment, ar.UpdateTime, ar.LatestChangeset)
		}
	} else {
		log.Printf("Изменений по '%s' за %v мин нет, последний чейнджсет %d", ar.Comment, ar.UpdateTime, ar.LatestChangeset)
	}
}

func (o *OCW) getHttp(client *http.Client, url string) ([]byte, error) {
	var content []byte

	response, err := client.Get(url)
	if err != nil {
		log.Println("Http error ", err)
		return nil, err
	}
	defer response.Body.Close()

	content, err = ioutil.ReadAll(response.Body)
	if err != nil {
		log.Fatal("Ioutil error ", err)
	}
	response.Body.Close()

	return content, nil
}

func (o *OCW) getChangeset(client *http.Client, num int64) (oapi.Changeset, error) {
	var (
		cgst oapi.Changeset
		err  error
		body []byte
	)
	body, err = o.getHttp(
		client,
		fmt.Sprintf(
			"http://www.openstreetmap.org/api/0.6/changeset/%v/download",
			num))
	if err != nil {
		return cgst, err
	}
	err = xml.Unmarshal(body, &cgst)
	if err != nil {
		return cgst, err
	}
	return cgst, err
}

// Разбирает структуру "области" и делает запрос к сайту ОСМ
// Отдает разобранный хмл чейнджсетов
func (o *OCW) getDWbody(a Area, client *http.Client) []oapi.ChangesetInfo {
	var (
		xmlContent []byte
		err        error
		osmresp    oapi.ChangesetList
	)

	xmlContent, err = o.getHttp(
		client,
		fmt.Sprintf(
			OSMAPIEndpoint,
			fmt.Sprintf(
				ChangesetRequestString,
				a.Bbox.Minlon,
				a.Bbox.Minlat,
				a.Bbox.Maxlon,
				a.Bbox.Maxlat,
				time.Now().Add(
					time.Duration(a.UpdateTime)*time.Minute*(-1)).
					In(time.FixedZone("0", 0)).Format(time.RFC3339))))

	if err == nil {
		if xml.Unmarshal(xmlContent, &osmresp) != nil {
			log.Println("Unmarshal error")
		}
	}
	return osmresp.Changesets
}

func (o *OCW) getMemString() string {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return fmt.Sprintf("Mem: A %4.3f HA %4.3f S %4.3f MB. Obj: %v Goroutines: %d",
		float32(mem.Alloc)/1024.0/1024.0,
		float32(mem.HeapAlloc)/1024.0/1024.0,
		float32(mem.Sys)/1024.0/1024.0,
		mem.HeapObjects,
		runtime.NumGoroutine())
}

func (o *OCW) memMonitor() {
	c := time.Tick(memMonitorTimeout * time.Second)
	for range c {
		runtime.GC()
		log.Println(o.getMemString())
	}
}

func (o *OCW) readConfig() {
	conffile, err := ioutil.ReadFile(configpath)

	if err != nil {
		log.Fatal("Ioutil error ", err)
	}

	json.Unmarshal(conffile, &o.areas)
}

func (o *OCW) writeConfig() {
	data, err := json.MarshalIndent(o.areas, "", "\t")
	if err != nil {
		log.Fatalf("Json marshal error: %v\n", err)
	}

	err = ioutil.WriteFile(configpath, data, 0644)
	if err != nil {
		log.Fatalf("Ошибка записи файла конфигурации: %v\n", err)
	}
	log.Println("Файл конфигурации обновлен на диске")
}

func (o *OCW) telegramMessageSender() {
	log.Printf("Запускаем службу отправки сообщений в Телеграм")
	for {
		o.sendToTelegram(<-o.toSendOSMDataChannel)
	}
}

func (o *OCW) sendToTelegram(cgs OsmResponse) {
	var (
		chst oapi.Changeset
		err  error
		bot  *tgbotapi.BotAPI
	)
	chst, err = o.getChangeset(o.httpclient, cgs.Resp.ChangesetId)
	if err != nil {
		return
	}
	_ = chst
	bot, err = tgbotapi.NewBotAPI(telegramkey)
	if err != nil {
		log.Panic("Неправильный ключ:", telegramkey, err)
	}
	log.Printf("Авторизовались на аккаунте %s", bot.Self.UserName)

	var tags string

	for _, v := range cgs.Resp.Tags {
		tags = tags + v.Key + " = " + v.Value + "\n"
	}

	//формируем строку изменений
	var ChangesString = ""
	//точка
	if (len(chst.CreatedNodes) != 0) || (len(chst.ModifiedNodes) != 0) || (len(chst.DeletedNodes) != 0) {
		ChangesString = "Тчк"
		if len(chst.CreatedNodes) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" созд: %v", len(chst.CreatedNodes))
		}
		if len(chst.ModifiedNodes) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" изм: %v", len(chst.ModifiedNodes))
		}
		if len(chst.DeletedNodes) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" удал: %v", len(chst.DeletedNodes))
		}
		ChangesString = ChangesString + " "
	}
	if (len(chst.CreatedWays) != 0) || (len(chst.ModifiedWays) != 0) || (len(chst.DeletedWays) != 0) {
		ChangesString = ChangesString + "Лин"
		if len(chst.CreatedWays) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" созд: %v", len(chst.CreatedWays))
		}
		if len(chst.ModifiedWays) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" изм: %v", len(chst.ModifiedWays))
		}
		if len(chst.DeletedWays) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" удал: %v", len(chst.DeletedWays))
		}
		ChangesString = ChangesString + " "
	}
	if (len(chst.CreatedRelations) != 0) || (len(chst.ModifiedRelations) != 0) || (len(chst.DeletedRelations) != 0) {
		ChangesString = ChangesString + "Отн"
		if len(chst.CreatedRelations) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" созд: %v", len(chst.CreatedRelations))
		}
		if len(chst.ModifiedRelations) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" изм: %v", len(chst.ModifiedRelations))
		}
		if len(chst.DeletedRelations) != 0 {
			ChangesString = ChangesString + fmt.Sprintf(" удал: %v", len(chst.DeletedRelations))
		}
		ChangesString = ChangesString + " "
	}

	//формируем сообщение в телеге
	msg := tgbotapi.NewMessageToChannel(
		cgs.Channel,
		fmt.Sprintf(
			messageString,
			cgs.Resp.ChangesetId,
			url.QueryEscape(cgs.Resp.User),
			cgs.Resp.User,
			fmt.Sprintf(ChangesString),
			tags)+
			intersectionCheck(
				cgs.Bbox,
				cgs.Resp.Minlat,
				cgs.Resp.Minlon,
				cgs.Resp.Maxlat,
				cgs.Resp.Maxlon))
	msg.ParseMode = "HTML"
	msg.DisableWebPagePreview = true
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		[]tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonURL("\xF0\x9F\x91\x80 OSM", fmt.Sprintf("http://www.openstreetmap.org/changeset/%[1]v", cgs.Resp.ChangesetId)),
			tgbotapi.NewInlineKeyboardButtonURL("\xF0\x9F\x91\x80 Achavi", fmt.Sprintf("http://overpass-api.de/achavi/?changeset=%[1]v", cgs.Resp.ChangesetId)),
			tgbotapi.NewInlineKeyboardButtonURL("\xF0\x9F\x91\x80 OSMCHA", fmt.Sprintf("https://osmcha.mapbox.com/%[1]v", cgs.Resp.ChangesetId)),
			tgbotapi.NewInlineKeyboardButtonURL("\xE2\x9C\x8F JOSM", fmt.Sprintf("http://127.0.0.1:8111/import?url=http://www.openstreetmap.org/changeset/%[1]v", cgs.Resp.ChangesetId))})
	_, err = bot.Send(msg)
	if err != nil {
		log.Printf("Сообщение не прошло", err.Error())
	}
}

func (o *OCW) telegrammAdminPanel() {
	bot, err := tgbotapi.NewBotAPI(telegramkey)
	if err != nil {
		log.Panic("Неправильный ключ:", telegramkey, err)
	}
	log.Printf("Авторизовались на аккаунте %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)
	for update := range updates {
		switch {
		// Пришло обычное сообщение
		case update.Message != nil:
			text := strings.ToLower(update.Message.Text)
			switch {
			case text == "/start":
				msg := tgbotapi.NewMessage(
					update.Message.Chat.ID,
					"Bot v. "+
						ocw_version+
						"\nMy commands:\n/mem   - used memory\n/areas   - watched areas\n/version   - bot version")
				bot.Send(msg)
				break
			case text == "/mem":
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, o.getMemString())
				bot.Send(msg)
				break
			case text == "/version":
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Bot v."+ocw_version)
				bot.Send(msg)
				break
			case text == "/areas":
				o.toReadConfigSignal <- true
				ar := <-o.toReadConfig
				text := ""
				for k, v := range ar.Areas {
					text = text + fmt.Sprintf(
						"\x23\xE2\x83\xA3 %v \n\xF0\x9F\x9A\xA9 '%s'\n\xE2\x97\xBB [%f, %f, %f, %f]\n\u23F3 %v minutes\n\xE2\x9C\x8F %v",
						k+1,
						v.Comment,
						v.Bbox.Minlat, v.Bbox.Minlon, v.Bbox.Maxlat, v.Bbox.Maxlon,
						v.UpdateTime,
						v.LatestChangeset)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
					bot.Send(msg)
				}
				break
			}
			log.Printf("Пришла команда %s", text)
			break
		}
	}
	//msg.ParseMode = "HTML"
	//msg.DisableWebPagePreview = true
}

func ina(a1, a2, c float64) bool {
	if (a1 < c) && (a2 > c) {
		return true
	}
	return false
}

func intersectionCheck(bbmain oapi.BoundsBox, Minlat, Minlon, Maxlat, Maxlon float64) string {
	if occurenceCheck(bbmain, Minlat, Minlon) && occurenceCheck(bbmain, Maxlat, Maxlon) {
		return ""
	}
	if occurenceCheck(bbmain, Minlat, Minlon) || occurenceCheck(bbmain, Maxlat, Maxlon) {
		return "\n<i>Частично в области отслеживания</i>"
	}
	return "\n<i>Больше области отслеживания</i>"
}

func occurenceCheck(bb oapi.BoundsBox, lat, lon float64) bool {
	if ina(bb.Minlat, bb.Maxlat, lat) && ina(bb.Minlon, bb.Maxlon, lon) {
		return true
	}
	return false
}
