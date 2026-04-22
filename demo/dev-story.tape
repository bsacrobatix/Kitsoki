# VHS tape for the dev-story demo.
# Paced deliberately slower than cloak.tape so each room's view is readable.
# Run: vhs demo/dev-story.tape
# Output: demo/dev-story.gif

Output demo/dev-story.gif

Set Width 1280
Set Height 900
Set FontSize 14
Set FontFamily "JetBrains Mono"
Set Theme "Molokai"
Set TypingSpeed 110ms

# Launch dev-story with the replay harness against the demo oracle.
Type "./hally run testdata/apps/dev-story/app.yaml --harness replay --oracle testdata/apps/dev-story/oracle.yaml"
Enter
Sleep 4s

# Turn 1: navigate to the workspace manager.
Type "open workspace manager"
Enter
Sleep 3500ms

# Turn 2: enter a workspace.
Type "enter my-api-project"
Enter
Sleep 3s

# Turn 3: confirm entry — workspace_loading → workspace_room.
Type "enter workspace"
Enter
Sleep 3500ms

# Turn 4: open the Terminal Room.
Type "open terminal"
Enter
Sleep 3s

# Turn 5: propose a shell command.
Type "run git status"
Enter
Sleep 3500ms

# Turn 6: refine the proposal (iterate before running).
Type "refine with --short"
Enter
Sleep 3500ms

# Turn 7: accept — transition to executing.
Type "accept"
Enter
Sleep 2500ms

# Turn 8: advance to the result view.
Type "accept"
Enter
Sleep 4s

# Turn 9: check the inbox.
Type "check my inbox"
Enter
Sleep 3500ms

# Turn 10: return to main.
Type "home"
Enter
Sleep 4s
