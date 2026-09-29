package cmd

import (
	"asmroner/internal/logger"
	"asmroner/internal/utils"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// clean 命令
// 递归删除指定目录下的所有 links.txt 和 download_scripts 目录，
// 用于 IDM 下载完成后的清理
var cleanCmd = &cobra.Command{
	Use:   "clean <目录>",
	Short: "递归删除目录下所有 links.txt 和下载脚本",
	Long: `
clean 命令递归清理指定目录（含所有子目录）中的：
  - links.txt
  - download_scripts 目录（idm_download / aria2_download 等脚本）

适用场景：
  - 使用 export / pick 导出链接并通过 IDM 下载完成后，一键清理残留文件

示例：
  asmroner clean ./downloads
  asmroner clean "D:\downloads\RJ01526160-xxx"
`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		dir := args[0]
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			logger.Fail("目录不存在: %s", dir)
			return
		}

		linkCount, scriptDirCount, err := utils.CleanExportArtifacts(dir)
		if err != nil {
			logger.Fail("遍历目录失败: %v", err)
			return
		}

		if linkCount == 0 && scriptDirCount == 0 {
			fmt.Println("未找到任何 links.txt 或 download_scripts")
		} else {
			logger.Done("已删除 %d 个 links.txt，%d 个 download_scripts 目录", linkCount, scriptDirCount)
		}
	},
}

func init() {
	RegisterCmd(cleanCmd)
}
