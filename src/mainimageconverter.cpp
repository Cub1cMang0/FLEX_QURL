#include "mainimageconverter.h"
#include "json.hpp"
#include <fstream>
#include <QDir>
#include <QFileInfo>
#include <QImage>
#include <QImageReader>
#include <QImageWriter>
#include <QPainter>
#include <QStandardPaths>

// set std and json
using json = nlohmann::json;
using namespace std;

MainImageConverter::MainImageConverter(QObject *parent)
    : ImageFileConverter(), QObject(parent) {}

// Main image conversion logic
void MainImageConverter::convert_image(const QString &input_path, const QString &output_path, const QString &input_ext, const QString &output_ext)
{
    // Extract input file info
    QFileInfo input_file_info(input_path);
    // Construcut output name
    QString output_name = input_file_info.completeBaseName() + "." + output_ext.toLower();
    // Reconstruct file output path
    QString complete_output = QDir(output_path).filePath(output_name);
    // Set grab capabilities base on output
    const auto &capabilities = image_capabilities[output_ext.toLower()];
    // Set Image and ImageReader variables
    QImage image;
    QImageReader reader(input_path);
    // Ensure the the reader is functioning
    if (!reader.canRead())
    {
        // Set eror message
        QString error = QString("Image could not be loaded: %1").arg(reader.errorString());
        // Emit signal update
        emit update_image_progress(error, false);
        return;
    }
    // Begin reading
    image = reader.read();
    // Ensure result isn't null
    if (image.isNull())
    {
        // Set error message
        QString error = QString("Image loading has failed: %1").arg(reader.errorString());
        // Emit signal update
        emit update_image_progress(error, false);
        return;
    }
    // Build and define configuration file directory (conversion_preferences.json)
    QString config_dir = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    // Define full config file path
    QString json_path = config_dir + "/conversion_preferences.json";
    // Attempt to fetch config file
    ifstream save_json(json_path.toStdString());
    int image_quality = -1;
    // Check if config file even exists
    if (!save_json.is_open())
    {
        // Define ImageWriter
        QImageWriter writer(complete_output);
        // Set output format
        writer.setFormat(output_ext.toLower().toUtf8());
        // Check if the conversion failed
        if (!writer.write(image))
        {
            // Set error message
            QString error_msg = QString("Image could not be converted: %1").arg(writer.errorString());
            // Emit signal update
            emit update_image_progress(error_msg, false);
            return;
        }
    }
    else
    {
        // Declare json object to store our conversion config file
        json load_data;
        // Move said config data to actual json object
        save_json >> load_data;
        // Finally close the ifstream 
        save_json.close();
        // Check if the user has conversion preferences for images
        if (load_data.contains("image"))
        {
            // Grab Image preferences from config file
            auto image_preferences = load_data["image"];
            // Check for any specified aspect ratio
            if (image_preferences["aspect_ratio"][0])
            {
                // Extract specified aspect ratio
                QString aspect_ratio = QString::fromStdString(image_preferences["aspect_ratio"][1]);
                // Define dimensions
                int width = image.width();
                int height = image.height();
                // Define current ratio
                double current_ratio = static_cast<double>(width) / height;
                // Account for colon
                int colon_location = aspect_ratio.indexOf(':');
                // Extract both numbers in the aspect ratio
                QString first_num = aspect_ratio.left(colon_location - 1);
                QString second_num = aspect_ratio.mid(colon_location + 2);
                // Define target ratio
                double target_ratio = static_cast<double>(first_num.toInt()) / second_num.toInt();
                int new_width = width;
                int new_height = height;
                if (current_ratio > target_ratio)
                {
                    // Chnage new height if current ratio is larger than target
                    new_height = static_cast<int>(width / target_ratio);
                }
                else if (current_ratio < target_ratio)
                {
                    // Chnage new width if current ratio is smaller than target
                    new_width = static_cast<int>(height * target_ratio);
                }
                // Create the new image
                QImage padded(new_width, new_height, image.format());
                padded.fill(Qt::transparent);
                // Define a Painter object using the new image
                QPainter painter(&padded);
                // Define x and y values for drawing image
                int x = (new_width - width) / 2;
                int y = (new_height - height) / 2;
                // Draw Image and end the painting
                painter.drawImage(x, y, image);
                painter.end();
                // Replace old image with new one
                image = padded;
            }
            if (capabilities.grayscale_support && image_preferences["grayscale"][0])
            {
                // Set gray scale if supported and selected
                image = image.convertToFormat(QImage::Format_Grayscale8);
            }
            if (capabilities.alpha_support && image.hasAlphaChannel() && image_preferences["alpha"][0])
            {
                // Set alpha if support and selected
                image = image.convertToFormat(QImage::Format_RGB32);
            }
            if (capabilities.bit_depth_support && image_preferences["bitdepth"][0])
            {
                // Extract and set bit depth if selected and supported
                int bit_depth = (QString::fromStdString(image_preferences["bitdepth"][1])).toInt();
                if (bit_depth == 1)
                {
                    image = image.convertToFormat(QImage::Format_Mono);
                }
                else if (bit_depth == 8)
                {
                    image = image.convertToFormat(QImage::Format_Indexed8);
                }
                else if (bit_depth == 24)
                {
                    image = image.convertToFormat(QImage::Format_RGB888);
                }
                else if (bit_depth == 32)
                {
                    image = image.convertToFormat(QImage::Format_RGB32);
                }
            }
            // Extract image quality
            QString quality = QString::fromStdString(image_preferences["quality"][1]);
            if (capabilities.quality_support && quality != "None")
            {
                // Set image quality from config
                image_quality = quality.toInt();
            }
        }
    }
    // Begin writing process
    QImageWriter writer(complete_output);
    writer.setFormat(output_ext.toLower().toUtf8());
    if (image_quality != -1)
    {
        // Set image quality from config if specified
        writer.setQuality(image_quality);
    }
    // If writing image fails
    if (!writer.write(image))
    {
        // Set error message
        QString error_msg = QString("Image could not be converted: %1").arg(writer.errorString());
        // Emit signal update
        emit update_image_progress(error_msg, false);
        return;
    }
    // Set result message
    QString result = QString("Success: %1.%2 has been converted to %3")
        .arg(input_file_info.completeBaseName()).arg(input_ext.toLower()).arg(output_name);
    // Emit signal update
    emit update_image_progress(result, true);
}
