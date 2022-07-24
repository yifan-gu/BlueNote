/*
Copyright © 2022 Yifan Gu <guyifan1121@gmail.com>

*/

package orgroam

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/google/uuid"

	"github.com/yifan-gu/BlueNote/pkg/config"
	"github.com/yifan-gu/BlueNote/pkg/exporter/orgroam/db"
	"github.com/yifan-gu/BlueNote/pkg/model"
	"github.com/yifan-gu/BlueNote/pkg/util"
)

type Location struct {
	Chapter  string
	Page     int
	Location int
}

func (l Location) String() string {
	if l.Chapter != "" {
		return fmt.Sprintf("Chapter: %s Page: %d Loc: %d", l.Chapter, l.Page, l.Location)
	}
	return fmt.Sprintf("Page: %d Loc: %d", l.Page, l.Location)
}

type Mark struct {
	Type     string
	Section  string
	Location Location
	Data     string
	Pos      int
	UUID     uuid.UUID
}

type Book struct {
	Title  string
	Author string
	Marks  []Mark
	UUID   uuid.UUID
}

func convertFromModelBook(book *model.Book) *Book {
	bk := &Book{
		Title:  book.Title,
		Author: book.Author,
	}
	for _, mk := range book.Marks {
		mark := Mark{
			Type:    model.MarkTypeString[mk.Type],
			Section: mk.Section,
			Data:    mk.Data,
			Location: Location{
				Chapter:  mk.Location.Chapter,
				Page:     mk.Location.Page,
				Location: mk.Location.Location,
			},
		}
		bk.Marks = append(bk.Marks, mark)
	}
	return bk
}

func (b *Book) exportOrgRoam(sp SqlPlanner, cfg *config.Config) ([]byte, error) {
	var orgTitleTpl, orgEntryTpl string

	if cfg.TemplateType < 0 || cfg.TemplateType > len(OrgTemplates) {
		orgTitleTpl = OrgTemplates[1].TitleTemplate
		orgEntryTpl = OrgTemplates[1].EntryTemplate
	}
	orgTitleTpl = OrgTemplates[cfg.TemplateType].TitleTemplate
	orgEntryTpl = OrgTemplates[cfg.TemplateType].EntryTemplate

	b.UUID = uuid.New()
	buf := new(bytes.Buffer)
	titleT := template.Must(template.New("template").Parse(orgTitleTpl))
	if err := titleT.Execute(buf, b); err != nil {
		return nil, fmt.Errorf("failed to execute org template for title: %v", err)
	}

	if err := sp.InsertNodeLinkTitleEntry(b, generateOutputPath(b, cfg)); err != nil {
		return nil, err
	}

	for _, mk := range b.Marks {
		mk.UUID = uuid.New()
		mk.Pos = len([]rune(buf.String())) + len("\n*")

		if err := sp.InsertNodeLinkMarkEntry(b, &mk, generateOutputPath(b, cfg)); err != nil {
			return nil, err
		}

		entryT := template.Must(template.New("template").Parse(orgEntryTpl))
		if err := entryT.Execute(buf, mk); err != nil {
			return nil, fmt.Errorf("failed to execute org template for entries: %v", err)
		}
	}

	return buf.Bytes(), nil
}

func generateOutputPath(b *Book, cfg *config.Config) string {
	filename := fmt.Sprintf("《%s》 by %s.org", b.Title, b.Author)
	if cfg.AuthorSubDir {
		return filepath.Join(cfg.OutputDir, b.Author, filename)
	}
	return filepath.Join(cfg.OutputDir, filename)
}

func writeRunesToFile(fullpath string, runes []rune) error {
	f, err := os.OpenFile(fullpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open or create file %s: %v", fullpath, err)
	}
	defer f.Close()

	buf := bufio.NewWriter(f)
	defer buf.Flush()

	for i := range runes {
		_, err = fmt.Fprintf(buf, "%c", runes[i])
		if err != nil {
			return fmt.Errorf("failed to write to file %s: %v", fullpath, err)
		}
	}
	return nil
}

type OrgRoamExporter struct{}

func (e *OrgRoamExporter) Name() string {
	return "orgroam"
}

func (e *OrgRoamExporter) Export(cfg *config.Config, book *model.Book) error {
	bk := convertFromModelBook(book)

	sq, err := db.NewSqlInterface(cfg.RoamDBPath, cfg.DBDriver)
	if err != nil {
		return err
	}
	defer sq.Close()

	fullpath, err := util.ResolvePath(generateOutputPath(bk, cfg))
	if err != nil {
		return err
	}
	dir := filepath.Dir(fullpath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		confirm, err := util.PromptConfirmation(cfg, fmt.Sprintf("directory %s doesn't exit, create?", dir))
		if err != nil {
			return err
		}
		if confirm {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create dir %q: %v", dir, err)
			}
		}
	}

	if _, err := os.Stat(fullpath); err == nil || !os.IsNotExist(err) {
		confirm, err := util.PromptConfirmation(cfg, fmt.Sprintf("file %s already exits, replace?", fullpath))
		if err != nil {
			return err
		}
		if !confirm {
			return nil
		}
	}

	sp := newSqlPlanner(sq, cfg.UpdateRoamDB)
	b, err := bk.exportOrgRoam(sp, cfg)
	if err != nil {
		return err
	}
	// Workaround the unicode encoding.
	if err := writeRunesToFile(fullpath, []rune(string(b))); err != nil {
		return err
	}

	if err := sp.InsertFileEntry(bk, fullpath); err != nil {
		return err
	}

	if err := sp.CommitSql(); err != nil {
		return err
	}

	fmt.Println("Successfully created:", fullpath)

	return nil
}