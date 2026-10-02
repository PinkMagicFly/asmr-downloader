package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestOrganizeMediaFiles(t *testing.T) {
	root := t.TempDir()

	// 图片：3 个，分布在不同子目录，应被重命名为 1,2,3（按路径排序）
	touch(t, filepath.Join(root, "b.png"))
	touch(t, filepath.Join(root, "sub", "a.jpg"))
	touch(t, filepath.Join(root, "sub", "deep", "c.webp"))
	// 视频：两个同名 → 都加 [重复] 前缀
	touch(t, filepath.Join(root, "v1.mp4"))
	touch(t, filepath.Join(root, "sub", "v1.mp4"))
	touch(t, filepath.Join(root, "v2.mkv"))
	// 音频：一个与目标目录已有文件同名
	touch(t, filepath.Join(root, "bgm.wav"))
	touch(t, filepath.Join(root, "sub", "bgm.wav"))
	touch(t, filepath.Join(root, "other.mp3"))
	// 非媒体文件不应被动
	touch(t, filepath.Join(root, "links.txt"))
	touch(t, filepath.Join(root, "sub", "readme.txt"))
	// 目标子文件夹中已有的文件：不应被当作待整理文件，但参与重名判定
	touch(t, filepath.Join(root, "图片", "old.png"))
	touch(t, filepath.Join(root, "音频", "stay.mp3"))

	res, err := OrganizeMediaFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Images != 3 || res.Videos != 3 || res.Audios != 3 {
		t.Fatalf("统计不符: %+v", res)
	}
	if res.Dupes != 4 { // v1.mp4 ×2 + bgm.wav ×2
		t.Fatalf("重名计数不符: %+v", res)
	}

	// 图片编号为 1,2,3，扩展名保留，old.png 不受影响
	for _, name := range []string{"1.png", "2.jpg", "3.webp", "old.png"} {
		if !exists(filepath.Join(root, "图片", name)) {
			t.Errorf("图片目录缺少 %s", name)
		}
	}
	// 原位置图片已被移走
	if exists(filepath.Join(root, "b.png")) || exists(filepath.Join(root, "sub", "a.jpg")) {
		t.Error("图片原文件未被移动")
	}

	// 视频：v2.mkv 原名，v1.mp4 两个都带 [重复] 前缀且互不覆盖
	if !exists(filepath.Join(root, "视频", "v2.mkv")) {
		t.Error("视频 v2.mkv 缺失")
	}
	videoEntries, _ := os.ReadDir(filepath.Join(root, "视频"))
	if len(videoEntries) != 3 {
		t.Fatalf("视频目录应有 3 个文件，实际 %d", len(videoEntries))
	}
	for _, e := range videoEntries {
		if e.Name() != "v2.mkv" && len(e.Name()) < len("[重复]") {
			t.Errorf("重名视频未加前缀: %s", e.Name())
		}
	}

	// 音频：bgm.wav 两个都带 [重复]，other.mp3 与已有的 stay.mp3 原名保留
	if !exists(filepath.Join(root, "音频", "other.mp3")) || !exists(filepath.Join(root, "音频", "stay.mp3")) {
		t.Error("音频原名文件缺失")
	}
	if exists(filepath.Join(root, "音频", "bgm.wav")) {
		t.Error("重名音频应保持加前缀，不应存在无前缀的 bgm.wav")
	}
	audioEntries, _ := os.ReadDir(filepath.Join(root, "音频"))
	if len(audioEntries) != 4 {
		t.Fatalf("音频目录应有 4 个文件，实际 %d", len(audioEntries))
	}

	// 非媒体文件全部被删除，root 下只剩三个目标子文件夹
	if exists(filepath.Join(root, "links.txt")) || exists(filepath.Join(root, "sub")) {
		t.Error("非媒体内容未被删除")
	}
	if res.Deleted != 2 { // links.txt + sub/readme.txt
		t.Fatalf("删除计数不符: %+v", res)
	}
	rootEntries, _ := os.ReadDir(root)
	if len(rootEntries) != 3 {
		names := []string{}
		for _, e := range rootEntries {
			names = append(names, e.Name())
		}
		t.Fatalf("root 应只剩 3 个子文件夹，实际: %v", names)
	}
	for _, e := range rootEntries {
		if e.Name() != "图片" && e.Name() != "视频" && e.Name() != "音频" {
			t.Errorf("root 下残留: %s", e.Name())
		}
	}
}

func TestOrganizeMediaFilesNotExist(t *testing.T) {
	if _, err := OrganizeMediaFiles(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("目录不存在时应返回错误")
	}
}
