package sandbox

import (
	"fmt"
	"os"
)

/*
 * MayReadFile says whether a step at level may read the file it has opened:
 * nil when it may.
 * desc: For a tool that reads files in its own code (file_read, file_list,
 *       archive, office_extract), which no lock holds. When the level allows
 *       ReadFiles every file may be read; below it, only what the operating
 *       system's own permissions give an ordinary account (ordinaryMayReadFile):
 *       on Linux the bits that apply to an account that owns nothing and is in
 *       no group, on Windows the access check made with the level-0 token.
 *       Made on the open file, not a name, so nothing can be put in its place
 *       between the check and the read. No name or path list.
 * param: level - the step's level.
 * param: f - the opened file or folder.
 * return: nil, or why not, naming the level reading every file needs.
 */
func MayReadFile(level int, f *os.File) error {
	eff := currentEffects()
	if eff.Allows(ReadFiles, level) {
		return nil
	}
	if err := ordinaryMayReadFile(f); err != nil {
		need := "it is never allowed"
		if l := eff.Level(ReadFiles); l != Never {
			need = fmt.Sprintf("reading every file needs level %d", l)
		}
		return fmt.Errorf("%s cannot be read at level %d, where only what an ordinary account may read is read (%v); %s",
			f.Name(), level, err, need)
	}
	return nil
}
