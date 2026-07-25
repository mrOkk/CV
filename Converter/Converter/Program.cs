using Markdig;
using Markdig.Syntax;
using Markdig.Syntax.Inlines;
using QuestPDF.Fluent;
using QuestPDF.Helpers;
using QuestPDF.Infrastructure;

QuestPDF.Settings.License = LicenseType.Community;

return ConverterApplication.Run(args);

internal static class ConverterApplication
{
    private const int UsageError = 2;
    private const int ConversionError = 1;

    public static int Run(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("Usage: Converter <input.md> <output.pdf>");
            return UsageError;
        }

        var inputPath = Path.GetFullPath(args[0]);
        var outputPath = Path.GetFullPath(args[1]);

        if (!File.Exists(inputPath))
        {
            Console.Error.WriteLine($"Input file was not found: {inputPath}");
            return UsageError;
        }

        if (!Path.GetExtension(inputPath).Equals(".md", StringComparison.OrdinalIgnoreCase))
        {
            Console.Error.WriteLine("The input file must have the .md extension.");
            return UsageError;
        }

        if (!Path.GetExtension(outputPath).Equals(".pdf", StringComparison.OrdinalIgnoreCase))
        {
            Console.Error.WriteLine("The output file must have the .pdf extension.");
            return UsageError;
        }

        try
        {
            var markdown = File.ReadAllText(inputPath);
            var document = Markdown.Parse(markdown);
            var outputDirectory = Path.GetDirectoryName(outputPath);

            if (!string.IsNullOrEmpty(outputDirectory))
            {
                Directory.CreateDirectory(outputDirectory);
            }

            new MarkdownPdfDocument(document, markdown).GeneratePdf(outputPath);
            Console.WriteLine($"PDF created: {outputPath}");
            return 0;
        }
        catch (Exception exception)
        {
            Console.Error.WriteLine($"Conversion failed: {exception.Message}");
            return ConversionError;
        }
    }
}

internal sealed class MarkdownPdfDocument(MarkdownDocument markdown, string source) : IDocument
{
    private const string PageBreakMarker = "<----->";

    public DocumentMetadata GetMetadata() => DocumentMetadata.Default;

    public void Compose(IDocumentContainer container)
    {
        container.Page(page =>
        {
            page.Size(PageSizes.A4);
            page.Margin(2, Unit.Centimetre);
            page.DefaultTextStyle(TextStyle.Default
                .FontFamily("Arial")
                .FontSize(11));

            page.Content().Column(column => RenderBlocks(column, markdown));

            // page.Footer().AlignCenter().Text(text =>
            // {
            //     text.Span("Page ");
            //     text.CurrentPageNumber();
            //     text.Span(" of ");
            //     text.TotalPages();
            // });
        });
    }

    private void RenderBlocks(
        ColumnDescriptor column,
        ContainerBlock blocks,
        TextStyle? paragraphStyle = null)
    {
        var resolvedParagraphStyle = paragraphStyle ?? TextStyle.Default.FontSize(11);

        foreach (var block in blocks)
        {
            if (IsPageBreak(block))
            {
                column.Item().PageBreak();
                continue;
            }

            switch (block)
            {
                case HeadingBlock heading:
                    RenderHeading(column, heading);
                    break;
                case ParagraphBlock paragraph:
                    column.Item()
                        .PaddingTop(0)
                        .PaddingBottom(2)
                        .Text(text => 
                            RenderInlines(text, paragraph.Inline, resolvedParagraphStyle));
                    break;
                case ListBlock list:
                    RenderList(column, list, resolvedParagraphStyle);
                    break;
                case CodeBlock code:
                    RenderCodeBlock(column, code);
                    break;
                default:
                    RenderUnsupportedBlock(column, block);
                    break;
            }
        }
    }

    private bool IsPageBreak(Block block)
    {
        if (block is CodeBlock)
        {
            return false;
        }

        var start = Math.Clamp(block.Span.Start, 0, source.Length);
        var end = Math.Clamp(block.Span.End + 1, start, source.Length);
        return source[start..end].Trim().Equals(PageBreakMarker, StringComparison.Ordinal);
    }

    private static void RenderHeading(ColumnDescriptor column, HeadingBlock heading)
    {
        var fontSize = heading.Level switch
        {
            1 => 22,
            2 => 18,
            3 => 15,
            _ => 13
        };

        column.Item()
            .PaddingTop(heading.Level == 1 ? 0 : 8)
            .PaddingBottom(6)
            .Text(text => RenderInlines(
                text,
                heading.Inline,
                TextStyle.Default.FontSize(fontSize).Bold()));
    }

    private void RenderList(ColumnDescriptor column, ListBlock list, TextStyle paragraphStyle)
    {
        var itemNumber = int.TryParse(list.OrderedStart, out var orderedStart) ? orderedStart : 1;

        foreach (var block in list)
        {
            if (block is not ListItemBlock item)
            {
                continue;
            }

            var marker = list.IsOrdered ? $"{itemNumber++}." : "•";
            column.Item().PaddingBottom(0).Row(row =>
            {
                row.ConstantItem(20).Text(marker);
                row.RelativeItem().Column(itemColumn =>
                    RenderBlocks(itemColumn, item, paragraphStyle));
            });
        }
    }

    private static void RenderCodeBlock(ColumnDescriptor column, CodeBlock code)
    {
        var codeText = string.Concat(code.Lines.Lines.Select(line => line.ToString()));
        column.Item()
            .PaddingBottom(8)
            .Background(Colors.Grey.Lighten3)
            .Padding(8)
            .Text(text => text.Span(codeText).FontFamily("Consolas").FontSize(9));
    }

    private void RenderUnsupportedBlock(ColumnDescriptor column, Block block)
    {
        var start = Math.Clamp(block.Span.Start, 0, source.Length);
        var end = Math.Clamp(block.Span.End + 1, start, source.Length);
        var originalText = source[start..end];

        if (!string.IsNullOrWhiteSpace(originalText))
        {
            column.Item().PaddingBottom(8).Text(originalText);
        }
    }

    private static void RenderInlines(TextDescriptor text, Inline? inline, TextStyle style, string? hyperlink = null)
    {
        for (var current = inline; current is not null; current = current.NextSibling)
        {
            switch (current)
            {
                case LiteralInline literal:
                    AddSpan(text, literal.Content.ToString(), style, hyperlink);
                    break;
                case LineBreakInline:
                    text.EmptyLine();
                    break;
                case CodeInline code:
                    AddSpan(text, code.Content, style.FontFamily("Consolas").FontSize(10), hyperlink);
                    break;
                case LinkInline link:
                    var destination = link.GetDynamicUrl != null ? link.GetDynamicUrl() : link.Url;
                    RenderInlines(
                        text,
                        link.FirstChild,
                        style.FontColor(Colors.Blue.Medium).Underline(),
                        string.IsNullOrWhiteSpace(destination) ? hyperlink : destination);
                    break;
                case EmphasisInline emphasis:
                    var emphasisStyle = emphasis.DelimiterCount >= 2 ? style.Bold() : style.Italic();
                    RenderInlines(text, emphasis.FirstChild, emphasisStyle, hyperlink);
                    break;
                case ContainerInline container:
                    RenderInlines(text, container.FirstChild, style, hyperlink);
                    break;
            }
        }
    }

    private static void AddSpan(TextDescriptor text, string content, TextStyle style, string? hyperlink)
    {
        if (string.IsNullOrWhiteSpace(hyperlink))
        {
            text.Span(content).Style(style);
            return;
        }

        text.Hyperlink(content, hyperlink).Style(style);
    }
}