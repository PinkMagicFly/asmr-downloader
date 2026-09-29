package cmd

import (
	"asmroner/internal/engine"
	"asmroner/internal/logger"
	"asmroner/internal/model"
	"asmroner/internal/utils"
	"asmroner/webui"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

var (
	pickPort      int
	pickOutputDir string
)

// pick 命令
// 启动一个本地网站：搜索 RJ 号 → 从 asmr.one 拉取文件树 →
// 网页勾选要下载的文件 → 生成目录结构 + links.txt + IDM 下载脚本
var pickCmd = &cobra.Command{
	Use:   "pick [作品RJID]",
	Short: "本地网站勾选要下载的文件，导出链接交给 IDM 下载",
	Long: `
pick 命令启动一个本地网站（自动打开浏览器），在网页中搜索 RJ 号，
从 asmr.one 拉取该作品的文件列表（仅文件结构，不含图片），
以文件树形式展示，默认全部勾选，你可以自由勾选/取消。

点击"生成下载任务"后，程序按勾选结果：
  1. 创建作品目录结构（未选中的文件不导出，全空的文件夹不创建）
  2. 在每个文件夹中写入 links.txt（仅包含选中的链接）
  3. 生成 download_scripts/idm_download.bat（双击后将任务添加到 IDM）
  4. 在作品根目录生成 cleanup_links.bat（下载完成后一键删除所有 links.txt）

网站会持续运行，可反复搜索多个作品，按 Ctrl+C 停止服务。

示例：
  asmroner pick                          # 启动网站
  asmroner pick RJ01526160               # 启动网站并直接加载该作品
  asmroner pick -o D:\downloads          # 指定输出目录
  asmroner pick -p 9990                  # 指定端口
`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		eng, err := engine.NewEngineManager(
			model.AppConfig.Limit.DownloadQPS, 1,
			model.AppConfig.Limit.DownloadJitterMin,
			model.AppConfig.Limit.DownloadJitterMax,
		)
		if err != nil {
			logger.Fail("初始化引擎失败: %v", err)
			return
		}

		// 已拉取的作品缓存：RJID -> 文件夹名 + 文件树 + 标题，避免导出/重复搜索时再次请求
		type cachedWork struct {
			folderName string
			title      string
			tracks     []model.Track
		}
		var cacheMu sync.Mutex
		workCache := make(map[string]cachedWork)

		gin.SetMode(gin.ReleaseMode)
		r := gin.New()
		r.Use(gin.Recovery())

		r.GET("/", func(c *gin.Context) {
			content, err := webui.GetFileContent("pick.html")
			if err != nil {
				c.String(http.StatusInternalServerError, "加载 pick.html 失败")
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", content)
		})

		// API: 按 RJ 号拉取作品文件树（命中缓存时直接返回）
		r.GET("/api/work", func(c *gin.Context) {
			rawID := strings.TrimSpace(c.Query("id"))
			valid, _, number, err := utils.IsValidDlsiteID(rawID)
			if err != nil || !valid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "无效的作品ID: " + rawID})
				return
			}
			cacheID := number // 缓存键统一用数字部分

			cacheMu.Lock()
			work, ok := workCache[cacheID]
			cacheMu.Unlock()
			if !ok {
				folderName, tracks, workInfo, err := eng.PrepareWorkExport(c.Request.Context(), rawID)
				if err != nil {
					c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
					return
				}
				work = cachedWork{folderName: folderName, title: workInfo.Title, tracks: tracks}
				cacheMu.Lock()
				workCache[cacheID] = work
				cacheMu.Unlock()
			}

			c.JSON(http.StatusOK, gin.H{
				"id":         cacheID,
				"title":      work.title,
				"folderName": work.folderName,
				"tree":       work.tracks,
			})
		})

		type exportRequest struct {
			ID       string   `json:"id"`
			Selected []string `json:"selected"`
			RunIDM   bool     `json:"run_idm"`
		}

		// API: 按勾选结果导出（可选：同时添加到 IDM 队列）
		r.POST("/api/export", func(c *gin.Context) {
			var req exportRequest
			if err := c.ShouldBindJSON(&req); err != nil || len(req.Selected) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效：未选中任何文件"})
				return
			}

			cacheKey := strings.TrimSpace(req.ID)
			if _, _, number, err := utils.IsValidDlsiteID(cacheKey); err == nil {
				cacheKey = number // 允许前端传完整 RJID，统一归一化为数字部分
			}
			cacheMu.Lock()
			work, ok := workCache[cacheKey]
			cacheMu.Unlock()
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请先搜索该作品，再生成下载任务"})
				return
			}

			selected := make(map[string]bool, len(req.Selected))
			for _, u := range req.Selected {
				selected[u] = true
			}

			workDir, folders, err := eng.ExportTracks(work.folderName, pickOutputDir, work.tracks, selected)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			total := 0
			for _, f := range folders {
				total += len(f.URLs)
			}
			logger.Done("%s 已导出 %d 个链接 -> %s", cacheKey, total, workDir)

			resp := gin.H{
				"dir":   workDir,
				"count": total,
			}
			// 直接调用 IDMan.exe 将任务添加到 IDM 队列（不经过脚本）
			if req.RunIDM {
				added, err := eng.EnqueueIDM(folders)
				if err != nil {
					logger.Warn("添加到 IDM 失败: %v", err)
					resp["idm_error"] = err.Error()
				} else {
					logger.Done("%s 已添加 %d 个任务到 IDM 队列", cacheKey, added)
					resp["idm_added"] = added
				}
			}
			c.JSON(http.StatusOK, resp)
		})

		// 输出根目录（清理脚本和作品目录的共同父目录）
		outputRoot := pickOutputDir
		if outputRoot == "" {
			outputRoot = "."
		}

		// API: 清理指定作品的 links.txt 和下载脚本
		r.POST("/api/clean", func(c *gin.Context) {
			var req struct {
				ID string `json:"id"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
				return
			}
			cacheKey := strings.TrimSpace(req.ID)
			if _, _, number, err := utils.IsValidDlsiteID(cacheKey); err == nil {
				cacheKey = number
			}
			cacheMu.Lock()
			work, ok := workCache[cacheKey]
			cacheMu.Unlock()
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请先搜索该作品"})
				return
			}

			target := filepath.Join(outputRoot, work.folderName)
			if _, err := os.Stat(target); os.IsNotExist(err) {
				c.JSON(http.StatusNotFound, gin.H{"error": "作品目录不存在，未执行任何操作: " + target})
				return
			}

			linkCount, scriptDirCount, err := utils.CleanExportArtifacts(target)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "清理失败: " + err.Error()})
				return
			}
			logger.Done("%s 清理完成: %d 个 links.txt，%d 个 download_scripts 目录", cacheKey, linkCount, scriptDirCount)
			c.JSON(http.StatusOK, gin.H{
				"dir":     target,
				"links":   linkCount,
				"scripts": scriptDirCount,
			})
		})

		addr := fmt.Sprintf(":%d", pickPort)
		srv := &http.Server{
			Addr:              addr,
			Handler:           r,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		}

		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("启动失败: %v", err)
			}
		}()

		// 延迟打开浏览器；带 RJID 参数时直接加载该作品
		go func() {
			time.Sleep(500 * time.Millisecond)
			link := fmt.Sprintf("http://localhost:%d", pickPort)
			if len(args) > 0 {
				link += "?id=" + strings.TrimSpace(args[0])
			}
			browser.OpenURL(link)
		}()

		logger.Info("选择网站已启动: http://localhost:%d", pickPort)
		logger.Info("在网页中搜索 RJ 号并勾选文件，按 Ctrl+C 停止服务")

		// 优雅退出
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
		<-quit
		logger.Warn("接收到退出信号，正在关闭服务...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("服务器强制关闭: %v", err)
		}
	},
}

func init() {
	pickCmd.Flags().IntVarP(&pickPort, "port", "p", 9998, "选择网站的本地服务端口")
	pickCmd.Flags().StringVarP(&pickOutputDir, "output", "o", "", "输出根目录路径（可选，默认为当前目录）")
	RegisterCmd(pickCmd)
}
