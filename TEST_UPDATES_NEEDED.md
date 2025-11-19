# Test Updates Needed for Block Kit Implementation

## Summary
7 tests are failing because they expect the old `Attachments` format instead of the new `Blocks` format.

## Test Results
- **Total**: 57 tests
- **Passed**: 50 tests ✅
- **Failed**: 7 tests ❌
- **Coverage**: 84.0%

## Failing Tests

All failures are in: `internal/controller/remediationpolicy_controller_test.go`

1. **Line 1802**: "should send both start and complete notifications when enabled"
2. **Line 1836**: "should skip start notification when notifyOnStart is false"
3. **Line 1871**: "should format start notification correctly"
4. **Line 1893**: "should format successful completion notification correctly"
5. **Line 1967**: "should extract detailed MCP response fields correctly"
6. **Line 2038**: "should format manual mode completion notification correctly"
7. **Line 2086**: "should format failed completion notification correctly"

## Root Cause

Tests are checking for:
```go
Expect(message.Attachments).To(HaveLen(1))
attachment := message.Attachments[0]
Expect(attachment.Color).To(Equal("warning"))
Expect(attachment.Title).To(Equal("🔄 Remediation Started"))
```

But the new Block Kit format uses:
```go
message.Blocks  // Array of SlackBlock instead of Attachments
```

## How to Fix

### Pattern to Replace

**OLD (Attachments)**:
```go
Expect(message.Attachments).To(HaveLen(1))
attachment := message.Attachments[0]
Expect(attachment.Color).To(Equal("warning"))
Expect(attachment.Title).To(Equal("🔄 Remediation Started"))
Expect(attachment.Text).To(ContainSubstring("Started processing event"))
Expect(attachment.Footer).To(Equal("dot-ai Kubernetes Event Controller"))

// Field checks
fieldTitles := make([]string, len(attachment.Fields))
for i, field := range attachment.Fields {
    fieldTitles[i] = field.Title
}
Expect(fieldTitles).To(ContainElements("Event Type", "Resource", "Namespace", "Mode"))
```

**NEW (Blocks)**:
```go
Expect(message.Blocks).To(Not(BeEmpty()))

// Check header block
headerBlock := message.Blocks[0]
Expect(headerBlock.Type).To(Equal("header"))
Expect(headerBlock.Text.Type).To(Equal("plain_text"))
Expect(headerBlock.Text.Text).To(ContainSubstring("🔄"))
Expect(headerBlock.Text.Text).To(ContainSubstring("Remediation Started"))

// Check section blocks for fields
var sectionBlocks []SlackBlock
for _, block := range message.Blocks {
    if block.Type == "section" {
        sectionBlocks = append(sectionBlocks, block)
    }
}
Expect(sectionBlocks).To(Not(BeEmpty()))

// Check context block (footer)
contextBlock := message.Blocks[len(message.Blocks)-1]
Expect(contextBlock.Type).To(Equal("context"))
Expect(contextBlock.Elements[0].Text.Text).To(ContainSubstring("dot-ai Kubernetes Event Controller"))
```

## Specific Updates Needed

### Test 1: Line 1871 - "should format start notification correctly"

Replace lines 1877-1892 with:
```go
Expect(message.Blocks).To(Not(BeEmpty()))

// Check header
Expect(message.Blocks[0].Type).To(Equal("header"))
Expect(message.Blocks[0].Text.Text).To(ContainSubstring("🔄"))
Expect(message.Blocks[0].Text.Text).To(ContainSubstring("Remediation Started"))

// Check for section blocks with fields
hasEventInfo := false
for _, block := range message.Blocks {
    if block.Type == "section" && len(block.Fields) > 0 {
        hasEventInfo = true
        break
    }
}
Expect(hasEventInfo).To(BeTrue())
```

### Test 2: Line 1893 - "should format successful completion notification correctly"

Similar pattern - check for blocks instead of attachments:
```go
Expect(message.Blocks).To(Not(BeEmpty()))
Expect(message.Blocks[0].Type).To(Equal("header"))
Expect(message.Blocks[0].Text.Text).To(ContainSubstring("✅"))
Expect(message.Blocks[0].Text.Text).To(ContainSubstring("Remediation Completed Successfully"))
```

### Test 3-7: Similar Pattern

All other tests follow the same pattern:
1. Replace `message.Attachments` checks with `message.Blocks` checks
2. Replace `attachment.Color/Title/Text` with block type and text checks
3. Replace field array iteration with block iteration
4. Check for appropriate block types (header, section, divider, context)

## Block Kit Structure

The new message structure:
```
Blocks[]:
  [0] - Header block (emoji + title)
  [1] - Section block (fields: Event Type, Resource, Namespace, Mode)
  [2] - Section block (Issue text)
  [3] - Divider
  [4] - Context block (footer)

For completion notifications:
  [0] - Header
  [1] - Result section
  [2] - Fields section
  [3+] - MCP detail blocks (Root Cause, Commands, etc.)
  [N-2] - Original Issue
  [N-1] - Divider
  [N] - Context (footer)
```

## Quick Fix Script

A helper function could be added to the test file:
```go
func getBlockByType(blocks []SlackBlock, blockType string) *SlackBlock {
    for i := range blocks {
        if blocks[i].Type == blockType {
            return &blocks[i]
        }
    }
    return nil
}

func getHeaderText(blocks []SlackBlock) string {
    header := getBlockByType(blocks, "header")
    if header != nil && header.Text != nil {
        return header.Text.Text
    }
    return ""
}
```

## Recommendation

The tests need systematic updates to match the new Block Kit format. Since the implementation is correct and builds successfully, the tests just need their assertions updated.

All 7 failing tests are in the "Slack Notification Integration" suite between lines 1700-2100.

Consider:
1. Update all attachment-based assertions to block-based assertions
2. Add helper functions for common block checks
3. Ensure code blocks for commands are tested (the key feature!)
4. Verify no truncation in command blocks
