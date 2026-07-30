// flex-convert-cli
// A headless command-line wrapper around FLEX's existing MainImageConverter.
// Same conversion class used by the desktop GUI, unmodified

#include <QGuiApplication>
#include <QFileInfo>
#include <QDebug>
#include "mainimageconverter.h"
#include "json.hpp"

using json = nlohmann::json;

int main(int argc, char *argv[])
{
    // Set QPA Platform to offscreen to avoid needing to specify each command call
    qputenv("QT_QPA_PLATFORM", "offscreen");
    // Set App
    QGuiApplication app(argc, argv);

    // Check for correct input count
    if (argc < 5)
    {
        qCritical().noquote() << "Usage: flex-convert-cli <input_file> <output_dir> <output_ext>";
        return 1;
    }

    // Extract each input field
    QString input_path = argv[1];
    QString output_dir  = argv[2];
    QString output_ext  = argv[3];
    QString input_ext   = QFileInfo(input_path).suffix();
    json load_data      = json::parse(argv[4]);

    bool ok = false;
    QString resultMessage;

    // Declare MainImageConverter var
    MainImageConverter converter;
    // Connect update_image_progress signal to listen for conversion result
    QObject::connect(&converter, &MainImageConverter::update_image_progress,
        [&](const QString &message, bool success)
        {
            ok = success;
            resultMessage = message;
        });
    
    // Convert image
    converter.convert_image(input_path, output_dir, input_ext, output_ext, load_data);

    
    qInfo().noquote() << resultMessage;
    return ok ? 0 : 1;
}
