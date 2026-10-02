package utils

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 整理时识别的媒体扩展名（小写，含点）
var (
	imageExts = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".bmp": true, ".avif": true, ".ico": true,
	}
	videoExts = map[string]bool{
		".mp4": true, ".webm": true, ".mkv": true, ".avi": true, ".mov": true,
		".wmv": true, ".flv": true, ".m4v": true, ".ts": true, ".mpg": true, ".mpeg": true,
	}
	audioExts = map[string]bool{
		".mp3": true, ".wav": true, ".flac": true, ".m4a": true,
		".ogg": true, ".aac": true, ".opus": true, ".wma": true,
	}
)

const (
	// OrganizeImageDir 等是整理时各媒体类型的目标子文件夹名
	OrganizeImageDir = "图片"
	OrganizeVideoDir = "视频"
	OrganizeAudioDir = "音频"
	// organizeDupePrefix 是视频/音频重名时添加的前缀
	organizeDupePrefix = "[重复]"
)

// OrganizeResult 记录一次目录整理的结果统计
type OrganizeResult struct {
	Images int // 移动的图片数（已按 1,2,3… 重命名）
	Videos int // 移动的视频数
	Audios int // 移动的音频数
	Dupes  int // 因重名被加上 [重复] 前缀的文件数（视频/音频）
}

// OrganizeMediaFiles 将 root 目录（含子目录，递归）中的媒体文件归类移动：
//   - 图片 → root/图片/，统一重命名为 1、2、3…（保留扩展名）
//   - 视频 → root/视频/，音频 → root/音频/，保持原名；
//     若有重名（含目标目录已有文件），重名的文件都加上 [重复] 前缀
//
// 已位于三个目标子文件夹内的文件不会重复处理。
func OrganizeMediaFiles(root string) (*OrganizeResult, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("目录不存在: %s", root)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("不是目录: %s", root)
	}
	root = filepath.Clean(root)
	targetDirs := map[string]bool{OrganizeImageDir: true, OrganizeVideoDir: true, OrganizeAudioDir: true}

	var images, videos, audios []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 跳过 root 直属的三个目标子文件夹，避免重复整理
			if path != root && filepath.Dir(path) == root && targetDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch {
		case imageExts[ext]:
			images = append(images, path)
		case videoExts[ext]:
			videos = append(videos, path)
		case audioExts[ext]:
			audios = append(audios, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("遍历目录失败: %w", err)
	}

	sort.Strings(images)
	sort.Strings(videos)
	sort.Strings(audios)

	res := &OrganizeResult{}

	if len(images) > 0 {
		dir := filepath.Join(root, OrganizeImageDir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("创建图片目录失败: %w", err)
		}
		// 统一编号命名；若目录里已有同编号文件则顺延，避免覆盖
		n := 1
		for _, src := range images {
			ext := strings.ToLower(filepath.Ext(src))
			var dest string
			for {
				dest = filepath.Join(dir, strconv.Itoa(n)+ext)
				if _, err := os.Stat(dest); os.IsNotExist(err) {
					break
				}
				n++
			}
			if err := moveFile(src, dest); err != nil {
				return res, fmt.Errorf("移动图片失败 %s: %w", src, err)
			}
			n++
			res.Images++
		}
	}

	if len(videos) > 0 {
		moved, dupes, err := moveKeepNames(videos, filepath.Join(root, OrganizeVideoDir))
		res.Videos, res.Dupes = moved, res.Dupes+dupes
		if err != nil {
			return res, err
		}
	}
	if len(audios) > 0 {
		moved, dupes, err := moveKeepNames(audios, filepath.Join(root, OrganizeAudioDir))
		res.Audios, res.Dupes = res.Audios+moved, res.Dupes+dupes
		if err != nil {
			return res, err
		}
	}

	return res, nil
}

// moveKeepNames 把 files 保持原名移动到 targetDir；
// 重名（多个待移动文件同名，或与 targetDir 中已有文件同名）时，
// 所有重名文件都加 [重复] 前缀；仍冲突时追加 " (2)" 等序号保证不覆盖。
func moveKeepNames(files []string, targetDir string) (moved, dupes int, err error) {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return 0, 0, fmt.Errorf("创建目录失败 %s: %w", targetDir, err)
	}

	// 统计每个文件名（小写）的出现次数：目标目录已有文件 + 待移动文件
	count := make(map[string]int)
	existing := make(map[string]string) // 小写文件名 -> 目标目录中已有文件的完整路径
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return 0, 0, fmt.Errorf("读取目录失败 %s: %w", targetDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		count[lower]++
		if _, ok := existing[lower]; !ok {
			existing[lower] = filepath.Join(targetDir, e.Name())
		}
	}
	for _, f := range files {
		count[strings.ToLower(filepath.Base(f))]++
	}

	for _, src := range files {
		base := filepath.Base(src)
		lower := strings.ToLower(base)
		destName := base
		if count[lower] > 1 {
			destName = organizeDupePrefix + base
			// 目标目录中已有的同名文件也加 [重复] 前缀
			if ep, ok := existing[lower]; ok && !strings.HasPrefix(filepath.Base(ep), organizeDupePrefix) {
				renamed := uniquePath(filepath.Join(targetDir, organizeDupePrefix+filepath.Base(ep)))
				if err := os.Rename(ep, renamed); err == nil {
					dupes++
				}
				delete(existing, lower)
			}
			dupes++
		}
		dest := uniquePath(filepath.Join(targetDir, destName))
		if err := moveFile(src, dest); err != nil {
			return moved, dupes, fmt.Errorf("移动文件失败 %s: %w", src, err)
		}
		moved++
	}
	return moved, dupes, nil
}

// uniquePath 若 path 已存在，在扩展名前追加 " (2)"、" (3)"… 直到不冲突
func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// moveFile 移动文件；跨盘符 os.Rename 失败时回退为复制+删除
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
