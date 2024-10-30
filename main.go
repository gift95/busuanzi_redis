package main

import (
	"context"
	"embed"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/thep0y/go-logger/log"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listening string `json:"listening"`
	Redis     struct {
		Addr     string `json:"addr"`
		Password string `json:"password"`
		Db       int    `json:"db"`
		Prefix   string `json:"prefix"`
	} `json:"redis"`
}

//go:embed src/config.yaml
var DefaultConfig embed.FS

var (
	config      Config
	RedisServer *redis.Client
)

func init() {
	log.ErrorPrefix.File = false
	loadConfig()
	initializeRedis()
}

func loadConfig() {
	if _, err := os.Stat("config.yaml"); os.IsNotExist(err) {
		createConfigFile()
	} else if err != nil {
		log.Fatal(err)
	}

	js, err := ioutil.ReadFile("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	if err := yaml.Unmarshal(js, &config); err != nil {
		log.Fatal(err)
	}

	if config.Redis.Prefix != "" && !strings.HasSuffix(config.Redis.Prefix, ":") {
		config.Redis.Prefix += ":"
	}
}

func createConfigFile() {
	cf, err := DefaultConfig.ReadFile("src/config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	if err := ioutil.WriteFile("config.yaml", cf, 0644); err != nil {
		log.Fatal(err)
	}

	log.Info("已创建配置文件，请修改后重新运行")
	os.Exit(0)
}

func initializeRedis() {
	RedisServer = redis.NewClient(&redis.Options{
		Addr:     config.Redis.Addr,
		Password: config.Redis.Password,
		DB:       config.Redis.Db,
	})

	for i := 0; i < 3; i++ {
		if _, err := RedisServer.Ping(context.Background()).Result(); err != nil {
			log.Errorf("Redis连接失败, %d 秒后重试, 剩余 %d 次", 5, 2-i)
			time.Sleep(5 * time.Second)
			continue
		}
		break
	}
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	server := gin.New()
	server.Use(cors.Default(), gin.Recovery(), logRequest())

	server.GET("/", handleRequest)

	log.Info("服务启动，监听 " + config.Listening)
	if err := server.Run(config.Listening); err != nil {
		log.Fatal(err)
	}
}

// logRequest 是一个中间件，用于记录请求信息
func logRequest() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录请求信息，包括域名
		log.Infof("IP: %s, Url: %s",c.ClientIP(), c.Request.Referer())
		c.Next() // 处理请求
	}
}




func handleRequest(c *gin.Context) {
	jsonpCallback := c.Query("jsonpCallback")
	if jsonpCallback == "" || c.Request.Referer() == "" {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    http.StatusNotFound,
			"message": "请求错误",
		})
		return
	}

	u, err := url.ParseRequestURI(c.Request.Referer())
	if err != nil {
		log.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    http.StatusInternalServerError,
			"message": "服务器内部错误",
		})
		return
	}
	host := u.Hostname()
	path := u.Path

	var (
		siteUV string
		sitePV string
		pagePV string
		wg     sync.WaitGroup
	)

	wg.Add(3)

	go func() {
		defer wg.Done()
		if err := recordSiteUV(host, c.ClientIP()); err != nil {
			log.Error(err)
			return
		}
		siteUV = getSiteUVCount(host)
	}()

	go func() {
		defer wg.Done()
		sitePV = strconv.FormatInt(incrementSitePV(host), 10)
	}()

	go func() {
		defer wg.Done()
		pagePV = strconv.FormatInt(incrementPagePV(host, path), 10)
	}()

	wg.Wait()
	c.Writer.WriteString(`try{` + jsonpCallback + `({"site_uv":` + siteUV + `,"page_pv":` + pagePV + `,"version":2.4,"site_pv":` + sitePV + `})}catch(e){}`)
}

func recordSiteUV(host, clientIP string) error {
	if err := RedisServer.SAdd(context.Background(), config.Redis.Prefix+"site_uv:"+host, clientIP).Err(); err != nil {
		return err
	}
	return nil
}

func getSiteUVCount(host string) string {
	suv, err := RedisServer.SCard(context.Background(), config.Redis.Prefix+"site_uv:"+host).Result()
	if err != nil {
		log.Error(err)
		return "0"
	}
	return strconv.FormatInt(suv, 10)
}

func incrementSitePV(host string) int64 {
	spv, err := RedisServer.HIncrBy(context.Background(), config.Redis.Prefix+"site_pv", host, 1).Result()
	if err != nil {
		log.Error(err)
		return 0
	}
	return spv
}

func incrementPagePV(host, path string) int64 {
	ppv, err := RedisServer.HIncrBy(context.Background(), config.Redis.Prefix+"page_pv:"+host, path, 1).Result()
	if err != nil {
		log.Error(err)
		return 0
	}
	return ppv
}
