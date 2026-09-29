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
	"strings"
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
// 在浏览器中以文件树形式勾选要下载的文件，
// 按勾选结果生成目录结构 + links.txt + IDM 下载脚本，下载任务交给 IDM 完成
var pickCmd = &cobra.Command{
	Use:   "pick <作品RJID>",
	Short: "网页勾选要下载的文件，导出链接交给 IDM 下载",
	Long: `
pick 命令会获取作品的完整文件列表，启动一个本地网页（自动打开浏览器），
以文件树形式展示所有文件，默认全部勾选，你可以自由勾选/取消。

点击"生成下载任务"后，程序按勾选结果：
  1. 创建作品目录结构（未选中的文件不导出，全空的文件夹不创建）
  2. 在每个文件夹中写入 links.txt（仅包含选中的链接）
  3. 生成 download_scripts/idm_download.bat（双击后将任务添加到 IDM）
  4. 在作品根目录生成 cleanup_links.bat（下载完成后一键删除所有 links.txt）

示例：
  asmroner pick RJ01526160
  asmroner pick RJ01526160 -o D:\downloads
  asmroner pick RJ01526160 -p 9990
`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		valid, prefix, number, err := utils.IsValidDlsiteID(id)
		if err != nil || !valid {
			logger.Fail("无效的作品ID: %s", id)
			return
		}

		eng, err := engine.NewEngineManager(10, 5, 500, 2000)
		if err != nil {
			logger.Fail("初始化引擎失败: %v", err)
			return
		}

		ctx := context.Background()

		// 获取作品信息与文件列表
		logger.Step("正在获取作品 %s 的文件列表...", strings.ToUpper(prefix)+number)
		workInfo, err := eng.GetWorkInfo(ctx, number)
		if err != nil {
			logger.Warn("获取作品信息失败: %v，将使用ID作为标题", err)
			workInfo = model.WorkInfo{Title: id, Release: ""}
		}
		tracks, err := eng.GetVoiceTracks(number)
		if err != nil {
			logger.Fail("获取文件列表失败: %v", err)
			return
		}

		folderName := utils.BuildFolderName(
			model.AppConfig.Downloader.FolderNameFormat,
			strings.ToUpper(prefix)+number,
			workInfo.Release,
			workInfo.HasSubtitle,
			workInfo.Title,
		)

		// 启动选择页面服务
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

		// API: 返回文件树
		r.GET("/api/tree", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"title":      workInfo.Title,
				"folderName": folderName,
				"tree":       tracks,
			})
		})

		type exportRequest struct {
			Selected []string `json:"selected"`
		}
		type exportResult struct {
			stats   map[string]int
			workDir string
			count   int
		}
		exported := make(chan exportResult, 1)

		// API: 按勾选结果导出
		r.POST("/api/export", func(c *gin.Context) {
			var req exportRequest
			if err := c.ShouldBindJSON(&req); err != nil || len(req.Selected) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效：未选中任何文件"})
				return
			}
			selected := make(map[string]bool, len(req.Selected))
			for _, u := range req.Selected {
				selected[u] = true
			}

			stats, workDir, err := eng.ExportTracks(folderName, pickOutputDir, tracks, selected)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			total := 0
			for _, n := range stats {
				total += n
			}
			c.JSON(http.StatusOK, gin.H{
				"dir":   workDir,
				"count": total,
			})
			exported <- exportResult{stats: stats, workDir: workDir, count: total}
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

		// 延迟打开浏览器
		go func() {
			time.Sleep(500 * time.Millisecond)
			browser.OpenURL(fmt.Sprintf("http://localhost:%d", pickPort))
		}()

		logger.Info("选择页面已启动: http://localhost:%d", pickPort)
		logger.Info("请在浏览器中勾选要下载的文件，然后点击\"生成下载任务\"；Ctrl+C 取消")

		// 等待导出完成或手动取消
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

		select {
		case <-quit:
			logger.Warn("已取消，未导出任何内容")
		case res := <-exported:
			logger.Done("成功导出 %d 个链接", res.count)
			fmt.Printf("作品目录: %s\n", res.workDir)
			fmt.Println("各文件夹链接数量:")
			for folder, count := range res.stats {
				display := folder
				if display == "" {
					display = "(根目录)"
				}
				fmt.Printf("  %s: %d 个链接\n", display, count)
			}
			fmt.Println("下一步:")
			fmt.Println("  1. 双击 download_scripts\\idm_download.bat 将任务添加到 IDM")
			fmt.Println("  2. 下载完成后双击 cleanup_links.bat 一键清理 links.txt")
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("服务器强制关闭: %v", err)
		}
	},
}

func init() {
	pickCmd.Flags().IntVarP(&pickPort, "port", "p", 9998, "选择页面的本地服务端口")
	pickCmd.Flags().StringVarP(&pickOutputDir, "output", "o", "", "输出根目录路径（可选，默认为当前目录）")
	RegisterCmd(pickCmd)
}
